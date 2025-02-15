package service

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/fleetdm/fleet/v4/server/contexts/capabilities"
	"github.com/fleetdm/fleet/v4/server/contexts/license"
	"github.com/fleetdm/fleet/v4/server/fleet"
	"github.com/fleetdm/fleet/v4/server/service/middleware/auth"
	"github.com/fleetdm/fleet/v4/server/service/middleware/endpoint_utils"
	"github.com/go-kit/kit/endpoint"
	kithttp "github.com/go-kit/kit/transport/http"
	"github.com/go-kit/log"
	"github.com/gorilla/mux"
)

type handlerFunc func(ctx context.Context, request interface{}, svc fleet.Service) (fleet.Errorer, error)

// A value that implements requestDecoder takes control of decoding the request
// as a whole - that is, it is responsible for decoding the body and any url
// or query argument itself.
type requestDecoder interface {
	DecodeRequest(ctx context.Context, r *http.Request) (interface{}, error)
}

// A value that implements bodyDecoder takes control of decoding the request
// body.
type bodyDecoder interface {
	DecodeBody(ctx context.Context, r io.Reader, u url.Values, c []*x509.Certificate) error
}

// makeDecoder creates a decoder for the type for the struct passed on. If the
// struct has at least 1 json tag it'll unmarshall the body. If the struct has
// a `url` tag with value list_options it'll gather fleet.ListOptions from the
// URL (similarly for host_options, carve_options, user_options that derive
// from the common list_options). Note that these behaviors do not work for embedded structs.
//
// Finally, any other `url` tag will be treated as a path variable (of the form
// /path/{name} in the route's path) from the URL path pattern, and it'll be
// decoded and set accordingly. Variables can be optional by setting the tag as
// follows: `url:"some-id,optional"`.
// The "list_options" are optional by default and it'll ignore the optional
// portion of the tag.
//
// If iface implements the requestDecoder interface, it returns a function that
// calls iface.DecodeRequest(ctx, r) - i.e. the value itself fully controls its
// own decoding.
//
// If iface implements the bodyDecoder interface, it calls iface.DecodeBody
// after having decoded any non-body fields (such as url and query parameters)
// into the struct.
func makeDecoder(iface interface{}) kithttp.DecodeRequestFunc {
	if iface == nil {
		return func(ctx context.Context, r *http.Request) (interface{}, error) {
			return nil, nil
		}
	}
	if rd, ok := iface.(requestDecoder); ok {
		return func(ctx context.Context, r *http.Request) (interface{}, error) {
			return rd.DecodeRequest(ctx, r)
		}
	}

	t := reflect.TypeOf(iface)
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("makeDecoder only understands structs, not %T", iface))
	}

	return func(ctx context.Context, r *http.Request) (interface{}, error) {
		v := reflect.New(t)
		nilBody := false

		var isBodyDecoder bool
		if _, ok := v.Interface().(bodyDecoder); ok {
			isBodyDecoder = true
		}

		buf := bufio.NewReader(r.Body)
		var body io.Reader = buf
		if _, err := buf.Peek(1); err == io.EOF {
			nilBody = true
		} else {
			if r.Header.Get("content-encoding") == "gzip" {
				gzr, err := gzip.NewReader(buf)
				if err != nil {
					return nil, endpoint_utils.BadRequestErr("gzip decoder error", err)
				}
				defer gzr.Close()
				body = gzr
			}

			if !isBodyDecoder {
				req := v.Interface()
				if err := json.NewDecoder(body).Decode(req); err != nil {
					return nil, endpoint_utils.BadRequestErr("json decoder error", err)
				}
				v = reflect.ValueOf(req)
			}
		}

		fields := endpoint_utils.AllFields(v)
		for _, fp := range fields {
			field := fp.V

			urlTagValue, ok := fp.Sf.Tag.Lookup("url")

			var err error
			if ok {
				optional := false
				urlTagValue, optional, err = endpoint_utils.ParseTag(urlTagValue)
				if err != nil {
					return nil, err
				}
				switch urlTagValue {
				case "list_options":
					opts, err := listOptionsFromRequest(r)
					if err != nil {
						return nil, err
					}
					field.Set(reflect.ValueOf(opts))

				case "user_options":
					opts, err := userListOptionsFromRequest(r)
					if err != nil {
						return nil, err
					}
					field.Set(reflect.ValueOf(opts))

				case "host_options":
					opts, err := hostListOptionsFromRequest(r)
					if err != nil {
						return nil, err
					}
					field.Set(reflect.ValueOf(opts))

				case "carve_options":
					opts, err := carveListOptionsFromRequest(r)
					if err != nil {
						return nil, err
					}
					field.Set(reflect.ValueOf(opts))

				default:
					err := endpoint_utils.DecodeURLTagValue(r, field, urlTagValue, optional)
					if err != nil {
						return nil, err
					}
					continue
				}
			}

			_, jsonExpected := fp.Sf.Tag.Lookup("json")
			if jsonExpected && nilBody {
				return nil, badRequest("Expected JSON Body")
			}

			err = endpoint_utils.DecodeQueryTagValue(r, fp)
			if err != nil {
				return nil, err
			}
		}

		if isBodyDecoder {
			bd := v.Interface().(bodyDecoder)
			var certs []*x509.Certificate
			if (r.TLS != nil) && (r.TLS.PeerCertificates != nil) {
				certs = r.TLS.PeerCertificates
			}

			if err := bd.DecodeBody(ctx, body, r.URL.Query(), certs); err != nil {
				return nil, err
			}
		}

		if !license.IsPremium(ctx) {
			for _, fp := range fields {
				if prem, ok := fp.Sf.Tag.Lookup("premium"); ok {
					val, err := strconv.ParseBool(prem)
					if err != nil {
						return nil, err
					}
					if val && !fp.V.IsZero() {
						return nil, &fleet.BadRequestError{Message: fmt.Sprintf(
							"option %s requires a premium license",
							fp.Sf.Name,
						)}
					}
					continue
				}
			}
		}

		return v.Interface(), nil
	}
}

func badRequest(msg string) error {
	return &fleet.BadRequestError{Message: msg}
}

type authEndpointer struct {
	svc               fleet.Service
	opts              []kithttp.ServerOption
	r                 *mux.Router
	authFunc          func(svc fleet.Service, next endpoint.Endpoint) endpoint.Endpoint
	versions          []string
	startingAtVersion string
	endingAtVersion   string
	alternativePaths  []string
	customMiddleware  []endpoint.Middleware
	usePathPrefix     bool
}

func newDeviceAuthenticatedEndpointer(svc fleet.Service, logger log.Logger, opts []kithttp.ServerOption, r *mux.Router, versions ...string) *authEndpointer {
	authFunc := func(svc fleet.Service, next endpoint.Endpoint) endpoint.Endpoint {
		return authenticatedDevice(svc, logger, next)
	}

	// Inject the fleet.CapabilitiesHeader header to the response for device endpoints
	opts = append(opts, capabilitiesResponseFunc(fleet.GetServerDeviceCapabilities()))
	// Add the capabilities reported by the device to the request context
	opts = append(opts, capabilitiesContextFunc())

	return &authEndpointer{
		svc:      svc,
		opts:     opts,
		r:        r,
		authFunc: authFunc,
		versions: versions,
	}
}

func newUserAuthenticatedEndpointer(svc fleet.Service, opts []kithttp.ServerOption, r *mux.Router, versions ...string) *authEndpointer {
	return &authEndpointer{
		svc:      svc,
		opts:     opts,
		r:        r,
		authFunc: auth.AuthenticatedUser,
		versions: versions,
	}
}

func newHostAuthenticatedEndpointer(svc fleet.Service, logger log.Logger, opts []kithttp.ServerOption, r *mux.Router, versions ...string) *authEndpointer {
	authFunc := func(svc fleet.Service, next endpoint.Endpoint) endpoint.Endpoint {
		return authenticatedHost(svc, logger, next)
	}
	return &authEndpointer{
		svc:      svc,
		opts:     opts,
		r:        r,
		authFunc: authFunc,
		versions: versions,
	}
}

func newOrbitAuthenticatedEndpointer(svc fleet.Service, logger log.Logger, opts []kithttp.ServerOption, r *mux.Router, versions ...string) *authEndpointer {
	authFunc := func(svc fleet.Service, next endpoint.Endpoint) endpoint.Endpoint {
		return authenticatedOrbitHost(svc, logger, next)
	}

	// Inject the fleet.Capabilities header to the response for Orbit hosts
	opts = append(opts, capabilitiesResponseFunc(fleet.GetServerOrbitCapabilities()))
	// Add the capabilities reported by Orbit to the request context
	opts = append(opts, capabilitiesContextFunc())

	return &authEndpointer{
		svc:      svc,
		opts:     opts,
		r:        r,
		authFunc: authFunc,
		versions: versions,
	}
}

func newNoAuthEndpointer(svc fleet.Service, opts []kithttp.ServerOption, r *mux.Router, versions ...string) *authEndpointer {
	return &authEndpointer{
		svc:      svc,
		opts:     opts,
		r:        r,
		authFunc: auth.UnauthenticatedRequest,
		versions: versions,
	}
}

var pathReplacer = strings.NewReplacer(
	"/", "_",
	"{", "_",
	"}", "_",
)

func getNameFromPathAndVerb(verb, path, startAt string) string {
	prefix := strings.ToLower(verb) + "_"
	if startAt != "" {
		prefix += pathReplacer.Replace(startAt) + "_"
	}
	return prefix + pathReplacer.Replace(strings.TrimPrefix(strings.TrimRight(path, "/"), "/api/_version_/fleet/"))
}

func capabilitiesResponseFunc(capabilities fleet.CapabilityMap) kithttp.ServerOption {
	return kithttp.ServerAfter(func(ctx context.Context, w http.ResponseWriter) context.Context {
		writeCapabilitiesHeader(w, capabilities)
		return ctx
	})
}

func capabilitiesContextFunc() kithttp.ServerOption {
	return kithttp.ServerBefore(capabilities.NewContext)
}

func writeCapabilitiesHeader(w http.ResponseWriter, capabilities fleet.CapabilityMap) {
	if len(capabilities) == 0 {
		return
	}

	w.Header().Set(fleet.CapabilitiesHeader, capabilities.String())
}

func writeBrowserSecurityHeaders(w http.ResponseWriter) {
	// Strict-Transport-Security informs browsers that the site should only be
	// accessed using HTTPS, and that any future attempts to access it using
	// HTTP should automatically be converted to HTTPS.
	w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains;")
	// X-Frames-Options disallows embedding the UI in other sites via <frame>,
	// <iframe>, <embed> or <object>, which can prevent attacks like
	// clickjacking.
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	// X-Content-Type-Options prevents browsers from trying to guess the MIME
	// type which can cause browsers to transform non-executable content into
	// executable content.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Referrer-Policy prevents leaking the origin of the referrer in the
	// Referer.
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
}

func (e *authEndpointer) POST(path string, f handlerFunc, v interface{}) {
	e.handleEndpoint(path, f, v, "POST")
}

func (e *authEndpointer) GET(path string, f handlerFunc, v interface{}) {
	e.handleEndpoint(path, f, v, "GET")
}

func (e *authEndpointer) PUT(path string, f handlerFunc, v interface{}) {
	e.handleEndpoint(path, f, v, "PUT")
}

func (e *authEndpointer) PATCH(path string, f handlerFunc, v interface{}) {
	e.handleEndpoint(path, f, v, "PATCH")
}

func (e *authEndpointer) DELETE(path string, f handlerFunc, v interface{}) {
	e.handleEndpoint(path, f, v, "DELETE")
}

func (e *authEndpointer) HEAD(path string, f handlerFunc, v interface{}) {
	e.handleEndpoint(path, f, v, "HEAD")
}

// PathHandler registers a handler for the verb and path. The pathHandler is
// a function that receives the actual path to which it will be mounted, and
// returns the actual http.Handler that will handle this endpoint. This is for
// when the handler needs to know on which path it was called.
func (e *authEndpointer) PathHandler(verb, path string, pathHandler func(path string) http.Handler) {
	e.handlePathHandler(path, pathHandler, verb)
}

func (e *authEndpointer) handlePathHandler(path string, pathHandler func(path string) http.Handler, verb string) {
	versions := e.versions
	if e.startingAtVersion != "" {
		startIndex := -1
		for i, version := range versions {
			if version == e.startingAtVersion {
				startIndex = i
				break
			}
		}
		if startIndex == -1 {
			panic("StartAtVersion is not part of the valid versions")
		}
		versions = versions[startIndex:]
	}
	if e.endingAtVersion != "" {
		endIndex := -1
		for i, version := range versions {
			if version == e.endingAtVersion {
				endIndex = i
				break
			}
		}
		if endIndex == -1 {
			panic("EndAtVersion is not part of the valid versions")
		}
		versions = versions[:endIndex+1]
	}

	// if a version doesn't have a deprecation version, or the ending version is the latest one, then it's part of the
	// latest
	if e.endingAtVersion == "" || e.endingAtVersion == e.versions[len(e.versions)-1] {
		versions = append(versions, "latest")
	}

	versionedPath := strings.Replace(path, "/_version_/", fmt.Sprintf("/{fleetversion:(?:%s)}/", strings.Join(versions, "|")), 1)
	nameAndVerb := getNameFromPathAndVerb(verb, path, e.startingAtVersion)
	if e.usePathPrefix {
		e.r.PathPrefix(versionedPath).Handler(pathHandler(versionedPath)).Name(nameAndVerb).Methods(verb)
	} else {
		e.r.Handle(versionedPath, pathHandler(versionedPath)).Name(nameAndVerb).Methods(verb)
	}
	for _, alias := range e.alternativePaths {
		nameAndVerb := getNameFromPathAndVerb(verb, alias, e.startingAtVersion)
		versionedPath := strings.Replace(alias, "/_version_/", fmt.Sprintf("/{fleetversion:(?:%s)}/", strings.Join(versions, "|")), 1)
		if e.usePathPrefix {
			e.r.PathPrefix(versionedPath).Handler(pathHandler(versionedPath)).Name(nameAndVerb).Methods(verb)
		} else {
			e.r.Handle(versionedPath, pathHandler(versionedPath)).Name(nameAndVerb).Methods(verb)
		}
	}
}

func (e *authEndpointer) handleHTTPHandler(path string, h http.Handler, verb string) {
	self := func(_ string) http.Handler { return h }
	e.handlePathHandler(path, self, verb)
}

func (e *authEndpointer) handleEndpoint(path string, f handlerFunc, v interface{}, verb string) {
	endpoint := e.makeEndpoint(f, v)
	e.handleHTTPHandler(path, endpoint, verb)
}

func (e *authEndpointer) makeEndpoint(f handlerFunc, v interface{}) http.Handler {
	next := func(ctx context.Context, request interface{}) (interface{}, error) {
		return f(ctx, request, e.svc)
	}
	endp := e.authFunc(e.svc, next)

	// apply middleware in reverse order so that the first wraps the second
	// wraps the third etc.
	for i := len(e.customMiddleware) - 1; i >= 0; i-- {
		mw := e.customMiddleware[i]
		endp = mw(endp)
	}

	return newServer(endp, makeDecoder(v), e.opts)
}

func (e *authEndpointer) StartingAtVersion(version string) *authEndpointer {
	ae := *e
	ae.startingAtVersion = version
	return &ae
}

func (e *authEndpointer) EndingAtVersion(version string) *authEndpointer {
	ae := *e
	ae.endingAtVersion = version
	return &ae
}

func (e *authEndpointer) WithAltPaths(paths ...string) *authEndpointer {
	ae := *e
	ae.alternativePaths = paths
	return &ae
}

func (e *authEndpointer) WithCustomMiddleware(mws ...endpoint.Middleware) *authEndpointer {
	ae := *e
	ae.customMiddleware = mws
	return &ae
}

func (e *authEndpointer) UsePathPrefix() *authEndpointer {
	ae := *e
	ae.usePathPrefix = true
	return &ae
}
