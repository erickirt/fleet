package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/fleetdm/fleet/v4/pkg/optjson"
	"github.com/fleetdm/fleet/v4/pkg/rawjson"
	"github.com/fleetdm/fleet/v4/server/authz"
	authz_ctx "github.com/fleetdm/fleet/v4/server/contexts/authz"
	"github.com/fleetdm/fleet/v4/server/contexts/ctxdb"
	"github.com/fleetdm/fleet/v4/server/contexts/ctxerr"
	"github.com/fleetdm/fleet/v4/server/contexts/license"
	"github.com/fleetdm/fleet/v4/server/contexts/viewer"
	"github.com/fleetdm/fleet/v4/server/fleet"
	"github.com/fleetdm/fleet/v4/server/version"
	"github.com/go-kit/log/level"
	"golang.org/x/text/unicode/norm"
)

////////////////////////////////////////////////////////////////////////////////
// Get AppConfig
////////////////////////////////////////////////////////////////////////////////

type appConfigResponse struct {
	fleet.AppConfig
	appConfigResponseFields
}

// appConfigResponseFields are grouped separately to aid with JSON unmarshaling
type appConfigResponseFields struct {
	UpdateInterval  *fleet.UpdateIntervalConfig  `json:"update_interval"`
	Vulnerabilities *fleet.VulnerabilitiesConfig `json:"vulnerabilities"`

	// License is loaded from the service
	License *fleet.LicenseInfo `json:"license,omitempty"`
	// Logging is loaded on the fly rather than from the database.
	Logging *fleet.Logging `json:"logging,omitempty"`
	// Email is returned when the email backend is something other than SMTP, for example SES
	Email *fleet.EmailConfig `json:"email,omitempty"`
	// SandboxEnabled is true if fleet serve was ran with server.sandbox_enabled=true
	SandboxEnabled bool                `json:"sandbox_enabled,omitempty"`
	Err            error               `json:"error,omitempty"`
	Partnerships   *fleet.Partnerships `json:"partnerships,omitempty"`
	// ConditionalAccess holds the Microsoft conditional access configuration.
	ConditionalAccess *fleet.ConditionalAccessSettings `json:"conditional_access,omitempty"`
}

// UnmarshalJSON implements the json.Unmarshaler interface to make sure we serialize
// both AppConfig and appConfigResponseFields properly:
//
// - If this function is not defined, AppConfig.UnmarshalJSON gets promoted and
// will be called instead.
// - If we try to unmarshal everything in one go, AppConfig.UnmarshalJSON doesn't get
// called.
func (r *appConfigResponse) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &r.AppConfig); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &r.appConfigResponseFields); err != nil {
		return err
	}
	return nil
}

// MarshalJSON implements the json.Marshaler interface to make sure we serialize
// both AppConfig and responseFields properly:
//
// - If this function is not defined, AppConfig.MarshalJSON gets promoted and
// will be called instead.
// - If we try to unmarshal everything in one go, AppConfig.MarshalJSON doesn't get
// called.
func (r appConfigResponse) MarshalJSON() ([]byte, error) {
	// Marshal only the response fields
	responseData, err := json.Marshal(r.appConfigResponseFields)
	if err != nil {
		return nil, err
	}

	// Marshal the base AppConfig
	appConfigData, err := json.Marshal(r.AppConfig)
	if err != nil {
		return nil, err
	}

	// we need to marshal and combine both groups separately because
	// AppConfig has a custom marshaler.
	return rawjson.CombineRoots(responseData, appConfigData)
}

func (r appConfigResponse) Error() error { return r.Err }

func getAppConfigEndpoint(ctx context.Context, request interface{}, svc fleet.Service) (fleet.Errorer, error) {
	vc, ok := viewer.FromContext(ctx)
	if !ok {
		return nil, errors.New("could not fetch user")
	}
	appConfig, err := svc.AppConfigObfuscated(ctx)
	if err != nil {
		return nil, err
	}
	license, err := svc.License(ctx)
	if err != nil {
		return nil, err
	}
	loggingConfig, err := svc.LoggingConfig(ctx)
	if err != nil {
		return nil, err
	}
	emailConfig, err := svc.EmailConfig(ctx)
	if err != nil {
		return nil, err
	}
	updateIntervalConfig, err := svc.UpdateIntervalConfig(ctx)
	if err != nil {
		return nil, err
	}
	vulnConfig, err := svc.VulnerabilitiesConfig(ctx)
	if err != nil {
		return nil, err
	}
	partnerships, err := svc.PartnershipsConfig(ctx)
	if err != nil {
		return nil, err
	}

	var conditionalAccessSettings *fleet.ConditionalAccessSettings
	conditionalAccessIntegration, err := svc.ConditionalAccessMicrosoftGet(ctx)
	if err != nil {
		return nil, err
	}
	if conditionalAccessIntegration != nil {
		conditionalAccessSettings = &fleet.ConditionalAccessSettings{
			MicrosoftEntraTenantID:             conditionalAccessIntegration.TenantID,
			MicrosoftEntraConnectionConfigured: conditionalAccessIntegration.SetupDone,
		}
	}

	isGlobalAdmin := vc.User.GlobalRole != nil && *vc.User.GlobalRole == fleet.RoleAdmin
	isAnyTeamAdmin := false
	if vc.User.Teams != nil {
		// check if the user is an admin for any team
		for _, team := range vc.User.Teams {
			if team.Role == fleet.RoleAdmin {
				isAnyTeamAdmin = true
				break
			}
		}
	}

	// Only admins should see SMTP and SSO settings
	var smtpSettings *fleet.SMTPSettings
	var ssoSettings *fleet.SSOSettings
	if isGlobalAdmin || isAnyTeamAdmin {
		smtpSettings = appConfig.SMTPSettings
		ssoSettings = appConfig.SSOSettings
	}

	// Only global admins should see osquery agent settings.
	var agentOptions *json.RawMessage
	if isGlobalAdmin {
		agentOptions = appConfig.AgentOptions
	}

	transparencyURL := fleet.DefaultTransparencyURL
	// Fleet Premium license is required for custom transparency url
	if license.IsPremium() && appConfig.FleetDesktop.TransparencyURL != "" {
		transparencyURL = appConfig.FleetDesktop.TransparencyURL
	}
	fleetDesktop := fleet.FleetDesktopSettings{TransparencyURL: transparencyURL}

	if appConfig.OrgInfo.ContactURL == "" {
		appConfig.OrgInfo.ContactURL = fleet.DefaultOrgInfoContactURL
	}

	features := appConfig.Features
	response := appConfigResponse{
		AppConfig: fleet.AppConfig{
			OrgInfo:                appConfig.OrgInfo,
			ServerSettings:         appConfig.ServerSettings,
			Features:               features,
			VulnerabilitySettings:  appConfig.VulnerabilitySettings,
			HostExpirySettings:     appConfig.HostExpirySettings,
			ActivityExpirySettings: appConfig.ActivityExpirySettings,

			SMTPSettings: smtpSettings,
			SSOSettings:  ssoSettings,
			AgentOptions: agentOptions,

			FleetDesktop: fleetDesktop,

			WebhookSettings: appConfig.WebhookSettings,
			Integrations:    appConfig.Integrations,
			MDM:             appConfig.MDM,
			Scripts:         appConfig.Scripts,
			UIGitOpsMode:    appConfig.UIGitOpsMode,
		},
		appConfigResponseFields: appConfigResponseFields{
			UpdateInterval:    updateIntervalConfig,
			Vulnerabilities:   vulnConfig,
			License:           license,
			Logging:           loggingConfig,
			Email:             emailConfig,
			SandboxEnabled:    svc.SandboxEnabled(),
			Partnerships:      partnerships,
			ConditionalAccess: conditionalAccessSettings,
		},
	}
	return response, nil
}

func (svc *Service) SandboxEnabled() bool {
	return svc.config.Server.SandboxEnabled
}

func (svc *Service) AppConfigObfuscated(ctx context.Context) (*fleet.AppConfig, error) {
	if !svc.authz.IsAuthenticatedWith(ctx, authz_ctx.AuthnDeviceToken) {
		if err := svc.authz.Authorize(ctx, &fleet.AppConfig{}, fleet.ActionRead); err != nil {
			return nil, err
		}
	}

	ac, err := svc.ds.AppConfig(ctx)
	if err != nil {
		return nil, err
	}

	ac.Obfuscate()

	return ac, nil
}

// //////////////////////////////////////////////////////////////////////////////
// Modify AppConfig
// //////////////////////////////////////////////////////////////////////////////

type modifyAppConfigRequest struct {
	Force     bool `json:"-" query:"force,optional"`     // if true, bypass strict incoming json validation
	DryRun    bool `json:"-" query:"dry_run,optional"`   // if true, apply validation but do not save changes
	Overwrite bool `json:"-" query:"overwrite,optional"` // if true, overwrite any existing settings with the incoming ones
	json.RawMessage
}

func modifyAppConfigEndpoint(ctx context.Context, request interface{}, svc fleet.Service) (fleet.Errorer, error) {
	req := request.(*modifyAppConfigRequest)
	appConfig, err := svc.ModifyAppConfig(ctx, req.RawMessage, fleet.ApplySpecOptions{
		Force:     req.Force,
		DryRun:    req.DryRun,
		Overwrite: req.Overwrite,
	})
	if err != nil {
		return appConfigResponse{appConfigResponseFields: appConfigResponseFields{Err: err}}, nil
	}

	// We do not use svc.License(ctx) to allow roles (like GitOps) write but not read access to AppConfig.
	license, _ := license.FromContext(ctx)

	loggingConfig, err := svc.LoggingConfig(ctx)
	if err != nil {
		return nil, err
	}
	response := appConfigResponse{
		AppConfig: *appConfig,
		appConfigResponseFields: appConfigResponseFields{
			License: license,
			Logging: loggingConfig,
		},
	}

	response.Obfuscate()

	if (!license.IsPremium()) || response.FleetDesktop.TransparencyURL == "" {
		response.FleetDesktop.TransparencyURL = fleet.DefaultTransparencyURL
	}

	return response, nil
}

func (svc *Service) ModifyAppConfig(ctx context.Context, p []byte, applyOpts fleet.ApplySpecOptions) (*fleet.AppConfig, error) {
	if err := svc.authz.Authorize(ctx, &fleet.AppConfig{}, fleet.ActionWrite); err != nil {
		return nil, err
	}

	// we need the config from the datastore because API tokens are obfuscated at
	// the service layer we will retrieve the obfuscated config before we return.
	// We bypass the mysql cache because this is a read that will be followed by
	// modifications and a save, so we need up-to-date data.
	ctx = ctxdb.BypassCachedMysql(ctx, true)
	appConfig, err := svc.ds.AppConfig(ctx)
	if err != nil {
		return nil, err
	}
	// the rest of the calls can use the cache safely (we read the AppConfig
	// again before returning, either after a dry-run or after saving the
	// AppConfig, in which case the cache will be up-to-date and safe to use).
	ctx = ctxdb.BypassCachedMysql(ctx, false)

	oldAppConfig := appConfig.Copy()

	// We do not use svc.License(ctx) to allow roles (like GitOps) write but not read access to AppConfig.
	license, _ := license.FromContext(ctx)

	var oldSMTPSettings fleet.SMTPSettings
	if appConfig.SMTPSettings != nil {
		oldSMTPSettings = *appConfig.SMTPSettings
	} else {
		// SMTPSettings used to be a non-pointer on previous iterations,
		// so if current SMTPSettings are not present (with empty values),
		// then this is a bug, let's log an error.
		level.Error(svc.logger).Log("msg", "smtp_settings are not present")
	}

	oldAgentOptions := ""
	if appConfig.AgentOptions != nil {
		oldAgentOptions = string(*appConfig.AgentOptions)
	}

	oldConditionalAccessEnabled := appConfig.Integrations.ConditionalAccessEnabled

	storedJiraByProjectKey, err := fleet.IndexJiraIntegrations(appConfig.Integrations.Jira)
	if err != nil {
		return nil, ctxerr.Wrap(ctx, err, "modify AppConfig")
	}

	storedZendeskByGroupID, err := fleet.IndexZendeskIntegrations(appConfig.Integrations.Zendesk)
	if err != nil {
		return nil, ctxerr.Wrap(ctx, err, "modify AppConfig")
	}

	invalid := &fleet.InvalidArgumentError{}
	var newAppConfig fleet.AppConfig
	if err := json.Unmarshal(p, &newAppConfig); err != nil {
		return nil, ctxerr.Wrap(ctx, &fleet.BadRequestError{
			Message:     "failed to decode app config",
			InternalErr: err,
		})
	}

	// default transparency URL is https://fleetdm.com/transparency so you are allowed to apply as long as it's not changing
	if newAppConfig.FleetDesktop.TransparencyURL != "" && newAppConfig.FleetDesktop.TransparencyURL != fleet.DefaultTransparencyURL {
		if !license.IsPremium() {
			invalid.Append("transparency_url", ErrMissingLicense.Error())
			return nil, ctxerr.Wrap(ctx, invalid)
		}
		if _, err := url.Parse(newAppConfig.FleetDesktop.TransparencyURL); err != nil {
			invalid.Append("transparency_url", err.Error())
			return nil, ctxerr.Wrap(ctx, invalid)
		}
	}

	if newAppConfig.SSOSettings != nil {
		validateSSOSettings(newAppConfig, appConfig, invalid, license)
		if invalid.HasErrors() {
			return nil, ctxerr.Wrap(ctx, invalid)
		}
	}

	// If we're in overwrite mode, clear out any feautures that are not explicitly specified.
	if applyOpts.Overwrite {
		appConfig.Features = newAppConfig.Features
		appConfig.SSOSettings = newAppConfig.SSOSettings
		appConfig.MDM.EndUserAuthentication = newAppConfig.MDM.EndUserAuthentication
	}

	// We apply the config that is incoming to the old one
	appConfig.EnableStrictDecoding()
	if err := json.Unmarshal(p, &appConfig); err != nil {
		err = fleet.NewUserMessageError(err, http.StatusBadRequest)
		return nil, ctxerr.Wrap(ctx, err)
	}

	// if turning off Windows MDM and Windows Migration is not explicitly set to
	// on in the same update, set it to off (otherwise, if it is explicitly set
	// to true, return an error that it can't be done when MDM is off, this is
	// addressed in validateMDM).
	if oldAppConfig.MDM.WindowsEnabledAndConfigured != appConfig.MDM.WindowsEnabledAndConfigured &&
		!appConfig.MDM.WindowsEnabledAndConfigured && !newAppConfig.MDM.WindowsMigrationEnabled {
		appConfig.MDM.WindowsMigrationEnabled = false
	}

	caStatus, err := svc.processAppConfigCAs(ctx, &newAppConfig, oldAppConfig, appConfig, invalid)
	if err != nil {
		return nil, ctxerr.Wrap(ctx, err, "processing AppConfig CAs")
	}

	// EnableDiskEncryption is an optjson.Bool field in order to support the
	// legacy field under "mdm.macos_settings". If the field provided to the
	// PATCH endpoint is set but invalid (that is, "enable_disk_encryption":
	// null) and no legacy field overwrites it, leave it unchanged (as if not
	// provided).

	// TODO: move this logic to the AppConfig unmarshaller? we need to do
	// this because we unmarshal twice into appConfig:
	//
	// 1. To get the JSON value from the database
	// 2. To update fields with the incoming values
	if newAppConfig.MDM.EnableDiskEncryption.Valid {
		if newAppConfig.MDM.EnableDiskEncryption.Value && svc.config.Server.PrivateKey == "" {
			return nil, ctxerr.New(ctx,
				"Missing required private key. Learn how to configure the private key here: https://fleetdm.com/learn-more-about/fleet-server-private-key")
		}
		appConfig.MDM.EnableDiskEncryption = newAppConfig.MDM.EnableDiskEncryption
	} else if appConfig.MDM.EnableDiskEncryption.Set && !appConfig.MDM.EnableDiskEncryption.Valid {
		appConfig.MDM.EnableDiskEncryption = oldAppConfig.MDM.EnableDiskEncryption
	}
	// this is to handle the case where `enable_release_device_manually: null` is
	// passed in the request payload, which should be treated as "not present/not
	// changed" by the PATCH. We should really try to find a more general way to
	// handle this.
	if !oldAppConfig.MDM.MacOSSetup.EnableReleaseDeviceManually.Valid {
		// this makes a DB migration unnecessary, will update the field to its default false value as necessary
		oldAppConfig.MDM.MacOSSetup.EnableReleaseDeviceManually = optjson.SetBool(false)
	}
	if newAppConfig.MDM.MacOSSetup.EnableReleaseDeviceManually.Valid {
		appConfig.MDM.MacOSSetup.EnableReleaseDeviceManually = newAppConfig.MDM.MacOSSetup.EnableReleaseDeviceManually
	} else {
		appConfig.MDM.MacOSSetup.EnableReleaseDeviceManually = oldAppConfig.MDM.MacOSSetup.EnableReleaseDeviceManually
	}
	if appConfig.MDM.MacOSSetup.ManualAgentInstall.Valid && appConfig.MDM.MacOSSetup.ManualAgentInstall.Value {
		if !license.IsPremium() {
			invalid.Append("macos_setup.manual_agent_install", ErrMissingLicense.Error())
			return nil, ctxerr.Wrap(ctx, invalid)
		}
	}

	var legacyUsedWarning error
	if legacyKeys := appConfig.DidUnmarshalLegacySettings(); len(legacyKeys) > 0 {
		// this "warning" is returned only in dry-run mode, and if no other errors
		// were encountered.
		legacyUsedWarning = &fleet.BadRequestError{
			Message: fmt.Sprintf("warning: deprecated settings were used in the configuration: %v; consider updating to the new settings: https://fleetdm.com/docs/using-fleet/configuration-files#settings",
				legacyKeys),
		}
	}

	// required fields must be set, ensure they haven't been removed by applying
	// the new config
	if appConfig.OrgInfo.OrgName == "" {
		invalid.Append("org_name", "organization name must be present")
	}
	if appConfig.ServerSettings.ServerURL == "" {
		invalid.Append("server_url", "Fleet server URL must be present")
	} else {
		if err := ValidateServerURL(appConfig.ServerSettings.ServerURL); err != nil {
			invalid.Append("server_url", "Couldn't update settings: "+err.Error())
		}
	}

	if appConfig.ActivityExpirySettings.ActivityExpiryEnabled && appConfig.ActivityExpirySettings.ActivityExpiryWindow < 1 {
		invalid.Append("activity_expiry_settings.activity_expiry_window", "must be greater than 0")
	}

	if appConfig.OrgInfo.ContactURL == "" {
		appConfig.OrgInfo.ContactURL = fleet.DefaultOrgInfoContactURL
	}

	if newAppConfig.AgentOptions != nil {
		// if there were Agent Options in the new app config, then it replaced the
		// agent options in the resulting app config, so validate those.
		if err := fleet.ValidateJSONAgentOptions(ctx, svc.ds, *appConfig.AgentOptions, license.IsPremium()); err != nil {
			err = fleet.SuggestAgentOptionsCorrection(err)
			err = fleet.NewUserMessageError(err, http.StatusBadRequest)
			if applyOpts.Force && !applyOpts.DryRun {
				level.Info(svc.logger).Log("err", err, "msg", "force-apply appConfig agent options with validation errors")
			}
			if !applyOpts.Force {
				return nil, ctxerr.Wrap(ctx, err, "validate agent options")
			}
		}
	}

	// If the license is Premium, we should always send usage statisics.
	if !license.IsAllowDisableTelemetry() {
		appConfig.ServerSettings.EnableAnalytics = true
	}

	fleet.ValidateGoogleCalendarIntegrations(appConfig.Integrations.GoogleCalendar, invalid)
	fleet.ValidateEnabledVulnerabilitiesIntegrations(appConfig.WebhookSettings.VulnerabilitiesWebhook, appConfig.Integrations, invalid)
	fleet.ValidateEnabledFailingPoliciesIntegrations(appConfig.WebhookSettings.FailingPoliciesWebhook, appConfig.Integrations, invalid)
	fleet.ValidateEnabledHostStatusIntegrations(appConfig.WebhookSettings.HostStatusWebhook, invalid)
	fleet.ValidateEnabledActivitiesWebhook(appConfig.WebhookSettings.ActivitiesWebhook, invalid)

	var conditionalAccessNoTeamUpdated bool
	if newAppConfig.Integrations.ConditionalAccessEnabled.Set {
		if err := fleet.ValidateConditionalAccessIntegration(ctx, svc, oldConditionalAccessEnabled.Value, newAppConfig.Integrations.ConditionalAccessEnabled.Value); err != nil {
			return nil, err
		}
		conditionalAccessNoTeamUpdated = oldConditionalAccessEnabled.Value != newAppConfig.Integrations.ConditionalAccessEnabled.Value
		appConfig.Integrations.ConditionalAccessEnabled = newAppConfig.Integrations.ConditionalAccessEnabled
	}

	if err := svc.validateMDM(ctx, license, &oldAppConfig.MDM, &appConfig.MDM, invalid); err != nil {
		return nil, ctxerr.Wrap(ctx, err, "validating MDM config")
	}

	abmAssignments, err := svc.validateABMAssignments(ctx, &newAppConfig.MDM, &oldAppConfig.MDM, invalid, license)
	if err != nil {
		return nil, ctxerr.Wrap(ctx, err, "validating ABM token assignments")
	}

	var vppAssignments map[uint][]uint
	vppAssignmentsDefined := newAppConfig.MDM.VolumePurchasingProgram.Set && newAppConfig.MDM.VolumePurchasingProgram.Valid
	if vppAssignmentsDefined {
		vppAssignments, err = svc.validateVPPAssignments(ctx, newAppConfig.MDM.VolumePurchasingProgram.Value, invalid, license)
		if err != nil {
			return nil, ctxerr.Wrap(ctx, err, "validating VPP token assignments")
		}
	}

	if invalid.HasErrors() {
		return nil, ctxerr.Wrap(ctx, invalid)
	}

	// ignore MDM.EnabledAndConfigured MDM.AppleBMTermsExpired, and MDM.AppleBMEnabledAndConfigured
	// if provided in the modify payload we don't return an error in this case because it would
	// prevent using the output of fleetctl get config as input to fleetctl apply or this endpoint.
	appConfig.MDM.AppleBMTermsExpired = oldAppConfig.MDM.AppleBMTermsExpired
	appConfig.MDM.AppleBMEnabledAndConfigured = oldAppConfig.MDM.AppleBMEnabledAndConfigured
	appConfig.MDM.EnabledAndConfigured = oldAppConfig.MDM.EnabledAndConfigured
	// ignore MDM.AndroidEnabledAndConfigured because it is set by the server only
	appConfig.MDM.AndroidEnabledAndConfigured = oldAppConfig.MDM.AndroidEnabledAndConfigured

	// do not send a test email in dry-run mode, so this is a good place to stop
	// (we also delete the removed integrations after that, which we don't want
	// to do in dry-run mode).
	if applyOpts.DryRun {
		if legacyUsedWarning != nil {
			return nil, legacyUsedWarning
		}

		// must reload to get the unchanged app config (retrieve with obfuscated secrets)
		obfuscatedAppConfig, err := svc.ds.AppConfig(ctx)
		if err != nil {
			return nil, err
		}
		obfuscatedAppConfig.Obfuscate()
		return obfuscatedAppConfig, nil
	}

	// Perform validation of the applied SMTP settings.
	if newAppConfig.SMTPSettings != nil {
		// Ignore the values for SMTPEnabled and SMTPConfigured.
		oldSMTPSettings.SMTPEnabled = appConfig.SMTPSettings.SMTPEnabled
		oldSMTPSettings.SMTPConfigured = appConfig.SMTPSettings.SMTPConfigured

		// If we enable SMTP and the settings have changed, then we send a test email.
		if appConfig.SMTPSettings.SMTPEnabled {
			if oldSMTPSettings != *appConfig.SMTPSettings || !appConfig.SMTPSettings.SMTPConfigured {
				if err = svc.sendTestEmail(ctx, appConfig); err != nil {
					return nil, fleet.NewInvalidArgumentError("SMTP Options", err.Error())
				}
			}
			appConfig.SMTPSettings.SMTPConfigured = true
		} else {
			appConfig.SMTPSettings.SMTPConfigured = false
		}
	}

	// NOTE: the frontend will always send all integrations back when making
	// changes, so as soon as Jira or Zendesk has something set, it's fair to
	// assume that integrations are being modified and we have the full set of
	// those integrations. When deleting, it does send empty arrays (not nulls),
	// so this is fine - e.g. when deleting the last integration it sends:
	//
	//   {"integrations":{"zendesk":[],"jira":[]}}
	//
	if newAppConfig.Integrations.Jira != nil || newAppConfig.Integrations.Zendesk != nil {
		delJira, err := fleet.ValidateJiraIntegrations(ctx, storedJiraByProjectKey, newAppConfig.Integrations.Jira)
		if err != nil {
			if errors.As(err, &fleet.IntegrationTestError{}) {
				return nil, ctxerr.Wrap(ctx, &fleet.BadRequestError{
					Message: err.Error(),
				})
			}
			return nil, ctxerr.Wrap(ctx, fleet.NewInvalidArgumentError("Jira integration", err.Error()))
		}
		appConfig.Integrations.Jira = newAppConfig.Integrations.Jira

		delZendesk, err := fleet.ValidateZendeskIntegrations(ctx, storedZendeskByGroupID, newAppConfig.Integrations.Zendesk)
		if err != nil {
			if errors.As(err, &fleet.IntegrationTestError{}) {
				return nil, ctxerr.Wrap(ctx, &fleet.BadRequestError{
					Message: err.Error(),
				})
			}
			return nil, ctxerr.Wrap(ctx, fleet.NewInvalidArgumentError("Zendesk integration", err.Error()))
		}
		appConfig.Integrations.Zendesk = newAppConfig.Integrations.Zendesk

		// if any integration was deleted, remove it from any team that uses it
		if len(delJira)+len(delZendesk) > 0 {
			if err := svc.ds.DeleteIntegrationsFromTeams(ctx, fleet.Integrations{Jira: delJira, Zendesk: delZendesk}); err != nil {
				return nil, ctxerr.Wrap(ctx, err, "delete integrations from teams")
			}
		}
	}
	// If google_calendar is null, we keep the existing setting. If it's not null, we update.
	if newAppConfig.Integrations.GoogleCalendar == nil {
		appConfig.Integrations.GoogleCalendar = oldAppConfig.Integrations.GoogleCalendar
	}

	gitopsModeEnabled, gitopsRepoURL := appConfig.UIGitOpsMode.GitopsModeEnabled, appConfig.UIGitOpsMode.RepositoryURL
	if gitopsModeEnabled {
		if !license.IsPremium() {
			return nil, fleet.NewInvalidArgumentError("UI GitOpsMode: ", ErrMissingLicense.Error())
		}
		if gitopsRepoURL == "" {
			return nil, fleet.NewInvalidArgumentError("UI GitOps Mode: ", "Repository URL is required when GitOps mode is enabled")
		}
	}

	if oldAppConfig.UIGitOpsMode.GitopsModeEnabled != appConfig.UIGitOpsMode.GitopsModeEnabled {
		// generate the activity
		var act fleet.ActivityDetails
		if gitopsModeEnabled {
			act = fleet.ActivityTypeEnabledGitOpsMode{}
		} else {
			act = fleet.ActivityTypeDisabledGitOpsMode{}
		}
		if err := svc.NewActivity(ctx, authz.UserFromContext(ctx), act); err != nil {
			return nil, ctxerr.Wrapf(ctx, err, "create activity %s", act.ActivityName())
		}

	}

	if !license.IsPremium() {
		// reset transparency url to empty for downgraded licenses
		appConfig.FleetDesktop.TransparencyURL = ""
	}

	if err := svc.ds.SaveAppConfig(ctx, appConfig); err != nil {
		return nil, err
	}

	// only create activities when config change has been persisted

	switch {
	case appConfig.WebhookSettings.ActivitiesWebhook.Enable && !oldAppConfig.WebhookSettings.ActivitiesWebhook.Enable:
		act := fleet.ActivityTypeEnabledActivityAutomations{WebhookUrl: appConfig.WebhookSettings.ActivitiesWebhook.DestinationURL}
		if err := svc.NewActivity(ctx, authz.UserFromContext(ctx), act); err != nil {
			return nil, ctxerr.Wrap(ctx, err, "create activity for enabled activity automations")
		}
	case !appConfig.WebhookSettings.ActivitiesWebhook.Enable && oldAppConfig.WebhookSettings.ActivitiesWebhook.Enable:
		act := fleet.ActivityTypeDisabledActivityAutomations{}
		if err := svc.NewActivity(ctx, authz.UserFromContext(ctx), act); err != nil {
			return nil, ctxerr.Wrap(ctx, err, "create activity for disabled activity automations")
		}
	case appConfig.WebhookSettings.ActivitiesWebhook.Enable &&
		appConfig.WebhookSettings.ActivitiesWebhook.DestinationURL != oldAppConfig.WebhookSettings.ActivitiesWebhook.DestinationURL:
		act := fleet.ActivityTypeEditedActivityAutomations{
			WebhookUrl: appConfig.WebhookSettings.ActivitiesWebhook.DestinationURL,
		}
		if err := svc.NewActivity(ctx, authz.UserFromContext(ctx), act); err != nil {
			return nil, ctxerr.Wrap(ctx, err, "create activity for edited activity automations")
		}
	}

	switch caStatus.ndes {
	case caStatusAdded:
		if err = svc.NewActivity(ctx, authz.UserFromContext(ctx), fleet.ActivityAddedNDESSCEPProxy{}); err != nil {
			return nil, ctxerr.Wrap(ctx, err, "create activity for added NDES SCEP proxy")
		}
	case caStatusEdited:
		if err = svc.NewActivity(ctx, authz.UserFromContext(ctx), fleet.ActivityEditedNDESSCEPProxy{}); err != nil {
			return nil, ctxerr.Wrap(ctx, err, "create activity for edited NDES SCEP proxy")
		}
	case caStatusDeleted:
		// Delete stored password
		if err := svc.ds.HardDeleteMDMConfigAsset(ctx, fleet.MDMAssetNDESPassword); err != nil {
			return nil, ctxerr.Wrap(ctx, err, "delete NDES SCEP password")
		}
		if err = svc.NewActivity(ctx, authz.UserFromContext(ctx), fleet.ActivityDeletedNDESSCEPProxy{}); err != nil {
			return nil, ctxerr.Wrap(ctx, err, "create activity for deleted NDES SCEP proxy")
		}
	default:
		// No change, no activity.
	}
	var caAssetsToDelete []string
	for caName, status := range caStatus.digicert {
		switch status {
		case caStatusAdded:
			if err = svc.NewActivity(ctx, authz.UserFromContext(ctx), fleet.ActivityAddedDigiCert{Name: caName}); err != nil {
				return nil, ctxerr.Wrap(ctx, err, "create activity for added DigiCert CA")
			}
		case caStatusEdited:
			if err = svc.NewActivity(ctx, authz.UserFromContext(ctx), fleet.ActivityEditedDigiCert{Name: caName}); err != nil {
				return nil, ctxerr.Wrap(ctx, err, "create activity for edited DigiCert CA")
			}
		case caStatusDeleted:
			if _, nameStillExists := caStatus.customSCEPProxy[caName]; !nameStillExists {
				caAssetsToDelete = append(caAssetsToDelete, caName)
			}
			if err = svc.NewActivity(ctx, authz.UserFromContext(ctx), fleet.ActivityDeletedDigiCert{Name: caName}); err != nil {
				return nil, ctxerr.Wrap(ctx, err, "create activity for deleted DigiCert CA")
			}
		}
	}
	for caName, status := range caStatus.customSCEPProxy {
		switch status {
		case caStatusAdded:
			if err = svc.NewActivity(ctx, authz.UserFromContext(ctx), fleet.ActivityAddedCustomSCEPProxy{Name: caName}); err != nil {
				return nil, ctxerr.Wrap(ctx, err, "create activity for added Custom SCEP Proxy")
			}
		case caStatusEdited:
			if err = svc.NewActivity(ctx, authz.UserFromContext(ctx), fleet.ActivityEditedCustomSCEPProxy{Name: caName}); err != nil {
				return nil, ctxerr.Wrap(ctx, err, "create activity for edited Custom SCEP Proxy")
			}
		case caStatusDeleted:
			if _, nameStillExists := caStatus.digicert[caName]; !nameStillExists {
				caAssetsToDelete = append(caAssetsToDelete, caName)
			}
			if err = svc.NewActivity(ctx, authz.UserFromContext(ctx), fleet.ActivityDeletedCustomSCEPProxy{Name: caName}); err != nil {
				return nil, ctxerr.Wrap(ctx, err, "create activity for deleted Custom SCEP Proxy")
			}
		}
	}
	if len(caAssetsToDelete) > 0 {
		err = svc.ds.DeleteCAConfigAssets(ctx, caAssetsToDelete)
		if err != nil {
			return nil, ctxerr.Wrap(ctx, err, "delete CA config assets")
		}
	}

	if oldAppConfig.MDM.MacOSSetup.MacOSSetupAssistant.Value != appConfig.MDM.MacOSSetup.MacOSSetupAssistant.Value &&
		appConfig.MDM.MacOSSetup.MacOSSetupAssistant.Value == "" {
		// clear macos setup assistant for no team - note that we cannot call
		// svc.DeleteMDMAppleSetupAssistant here as it would call the (non-premium)
		// current service implementation. We have to go through the Enterprise
		// extensions.
		if err := svc.EnterpriseOverrides.DeleteMDMAppleSetupAssistant(ctx, nil); err != nil {
			return nil, ctxerr.Wrap(ctx, err, "delete macos setup assistant")
		}
	}

	if oldAppConfig.MDM.MacOSSetup.BootstrapPackage.Value != appConfig.MDM.MacOSSetup.BootstrapPackage.Value &&
		appConfig.MDM.MacOSSetup.BootstrapPackage.Value == "" {
		// clear bootstrap package for no team - note that we cannot call
		// svc.DeleteMDMAppleBootstrapPackage here as it would call the (non-premium)
		// current service implementation. We have to go through the Enterprise
		// extensions.
		if err := svc.EnterpriseOverrides.DeleteMDMAppleBootstrapPackage(ctx, nil, applyOpts.DryRun); err != nil {
			return nil, ctxerr.Wrap(ctx, err, "delete Apple bootstrap package")
		}
	}

	tokensInCfg := make(map[string]struct{})
	for _, t := range newAppConfig.MDM.AppleBusinessManager.Value {
		tokensInCfg[t.OrganizationName] = struct{}{}
	}

	toks, err := svc.ds.ListABMTokens(ctx)
	if err != nil {
		return nil, ctxerr.Wrap(ctx, err, "listing ABM tokens")
	}

	if newAppConfig.MDM.AppleBusinessManager.Set && len(newAppConfig.MDM.AppleBusinessManager.Value) == 0 {
		for _, tok := range toks {
			if _, ok := tokensInCfg[tok.OrganizationName]; !ok {
				tok.MacOSDefaultTeamID = nil
				tok.IOSDefaultTeamID = nil
				tok.IPadOSDefaultTeamID = nil
				if err := svc.ds.SaveABMToken(ctx, tok); err != nil {
					return nil, ctxerr.Wrap(ctx, err, "saving ABM token assignments")
				}
			}
		}
	}

	if (appConfig.MDM.AppleBusinessManager.Set && appConfig.MDM.AppleBusinessManager.Valid) || appConfig.MDM.DeprecatedAppleBMDefaultTeam != "" {
		for _, tok := range abmAssignments {
			if err := svc.ds.SaveABMToken(ctx, tok); err != nil {
				return nil, ctxerr.Wrap(ctx, err, "saving ABM token assignments")
			}
		}
	}

	if vppAssignmentsDefined {
		// 1. Reset teams for VPP tokens that exist in Fleet but aren't present in the config being passed
		clear(tokensInCfg)
		for _, t := range newAppConfig.MDM.VolumePurchasingProgram.Value {
			tokensInCfg[t.Location] = struct{}{}
		}
		vppToks, err := svc.ds.ListVPPTokens(ctx)
		if err != nil {
			return nil, ctxerr.Wrap(ctx, err, "listing VPP tokens")
		}
		for _, tok := range vppToks {
			if _, ok := tokensInCfg[tok.Location]; !ok {
				tok.Teams = nil
				if _, err := svc.ds.UpdateVPPTokenTeams(ctx, tok.ID, nil); err != nil {
					return nil, ctxerr.Wrap(ctx, err, "saving VPP token teams")
				}
			}
		}
		// 2. Set VPP assignments that are defined in the config.
		for tokenID, tokenTeams := range vppAssignments {
			if _, err := svc.ds.UpdateVPPTokenTeams(ctx, tokenID, tokenTeams); err != nil {
				var errTokConstraint fleet.ErrVPPTokenTeamConstraint
				if errors.As(err, &errTokConstraint) {
					return nil, ctxerr.Wrap(ctx, fleet.NewUserMessageError(errTokConstraint, http.StatusConflict))
				}
				return nil, ctxerr.Wrap(ctx, err, "saving ABM token assignments")
			}
		}
	}

	// retrieve new app config with obfuscated secrets
	obfuscatedAppConfig, err := svc.ds.AppConfig(ctx)
	if err != nil {
		return nil, err
	}
	obfuscatedAppConfig.Obfuscate()

	// if the agent options changed, create the corresponding activity
	newAgentOptions := ""
	if obfuscatedAppConfig.AgentOptions != nil {
		newAgentOptions = string(*obfuscatedAppConfig.AgentOptions)
	}
	if oldAgentOptions != newAgentOptions {
		if err := svc.NewActivity(
			ctx,
			authz.UserFromContext(ctx),
			fleet.ActivityTypeEditedAgentOptions{
				Global: true,
			},
		); err != nil {
			return nil, ctxerr.Wrap(ctx, err, "create activity for app config agent options modification")
		}
	}

	//
	// Process OS updates config changes for Apple devices.
	//
	if err := svc.processAppleOSUpdateSettings(ctx, license, fleet.MacOS,
		oldAppConfig.MDM.MacOSUpdates,
		appConfig.MDM.MacOSUpdates,
	); err != nil {
		return nil, ctxerr.Wrap(ctx, err, "process macOS OS updates config change")
	}
	if err := svc.processAppleOSUpdateSettings(ctx, license, fleet.IOS,
		oldAppConfig.MDM.IOSUpdates,
		appConfig.MDM.IOSUpdates,
	); err != nil {
		return nil, ctxerr.Wrap(ctx, err, "process iOS OS updates config change")
	}
	if err := svc.processAppleOSUpdateSettings(ctx, license, fleet.IPadOS,
		oldAppConfig.MDM.IPadOSUpdates,
		appConfig.MDM.IPadOSUpdates,
	); err != nil {
		return nil, ctxerr.Wrap(ctx, err, "process iPadOS OS updates config change")
	}

	if appConfig.YaraRules != nil {
		if err := svc.ds.ApplyYaraRules(ctx, appConfig.YaraRules); err != nil {
			return nil, ctxerr.Wrap(ctx, err, "save yara rules for app config")
		}
	}

	// if the Windows updates requirements changed, create the corresponding
	// activity.
	if !oldAppConfig.MDM.WindowsUpdates.Equal(appConfig.MDM.WindowsUpdates) {
		var deadline, grace *int
		if appConfig.MDM.WindowsUpdates.DeadlineDays.Valid {
			deadline = &appConfig.MDM.WindowsUpdates.DeadlineDays.Value
		}
		if appConfig.MDM.WindowsUpdates.GracePeriodDays.Valid {
			grace = &appConfig.MDM.WindowsUpdates.GracePeriodDays.Value
		}

		if deadline != nil {
			if err := svc.EnterpriseOverrides.MDMWindowsEnableOSUpdates(ctx, nil, appConfig.MDM.WindowsUpdates); err != nil {
				return nil, ctxerr.Wrap(ctx, err, "enable no-team windows OS updates")
			}
		} else if err := svc.EnterpriseOverrides.MDMWindowsDisableOSUpdates(ctx, nil); err != nil {
			return nil, ctxerr.Wrap(ctx, err, "disable no-team windows OS updates")
		}

		if err := svc.NewActivity(
			ctx,
			authz.UserFromContext(ctx),
			fleet.ActivityTypeEditedWindowsUpdates{
				DeadlineDays:    deadline,
				GracePeriodDays: grace,
			},
		); err != nil {
			return nil, ctxerr.Wrap(ctx, err, "create activity for app config macos min version modification")
		}
	}

	if appConfig.MDM.EnableDiskEncryption.Valid && oldAppConfig.MDM.EnableDiskEncryption.Value != appConfig.MDM.EnableDiskEncryption.Value {
		if oldAppConfig.MDM.EnabledAndConfigured {
			var act fleet.ActivityDetails
			if appConfig.MDM.EnableDiskEncryption.Value {
				act = fleet.ActivityTypeEnabledMacosDiskEncryption{}
				if err := svc.EnterpriseOverrides.MDMAppleEnableFileVaultAndEscrow(ctx, nil); err != nil {
					return nil, ctxerr.Wrap(ctx, err, "enable no-team filevault and escrow")
				}
			} else {
				act = fleet.ActivityTypeDisabledMacosDiskEncryption{}
				if err := svc.EnterpriseOverrides.MDMAppleDisableFileVaultAndEscrow(ctx, nil); err != nil {
					return nil, ctxerr.Wrap(ctx, err, "disable no-team filevault and escrow")
				}
			}
			if err := svc.NewActivity(ctx, authz.UserFromContext(ctx), act); err != nil {
				return nil, ctxerr.Wrap(ctx, err, "create activity for app config macos disk encryption")
			}
		}
	}

	mdmEnableEndUserAuthChanged := oldAppConfig.MDM.MacOSSetup.EnableEndUserAuthentication != appConfig.MDM.MacOSSetup.EnableEndUserAuthentication
	if mdmEnableEndUserAuthChanged {
		var act fleet.ActivityDetails
		if appConfig.MDM.MacOSSetup.EnableEndUserAuthentication {
			act = fleet.ActivityTypeEnabledMacosSetupEndUserAuth{}
		} else {
			act = fleet.ActivityTypeDisabledMacosSetupEndUserAuth{}
		}
		if err := svc.NewActivity(ctx, authz.UserFromContext(ctx), act); err != nil {
			return nil, ctxerr.Wrap(ctx, err, "create activity for macos enable end user auth change")
		}
	}

	mdmSSOSettingsChanged := oldAppConfig.MDM.EndUserAuthentication.SSOProviderSettings !=
		appConfig.MDM.EndUserAuthentication.SSOProviderSettings
	serverURLChanged := oldAppConfig.ServerSettings.ServerURL != appConfig.ServerSettings.ServerURL
	appleMDMUrlChanged := oldAppConfig.MDMUrl() != appConfig.MDMUrl()
	if (mdmEnableEndUserAuthChanged || mdmSSOSettingsChanged || serverURLChanged || appleMDMUrlChanged) && license.IsPremium() {
		if err := svc.EnterpriseOverrides.MDMAppleSyncDEPProfiles(ctx); err != nil {
			return nil, ctxerr.Wrap(ctx, err, "sync DEP profiles")
		}
	}

	// if Windows MDM was enabled or disabled, create the corresponding activity
	if oldAppConfig.MDM.WindowsEnabledAndConfigured != appConfig.MDM.WindowsEnabledAndConfigured {
		var act fleet.ActivityDetails
		if appConfig.MDM.WindowsEnabledAndConfigured {
			act = fleet.ActivityTypeEnabledWindowsMDM{}
		} else {
			act = fleet.ActivityTypeDisabledWindowsMDM{}
		}
		if err := svc.NewActivity(ctx, authz.UserFromContext(ctx), act); err != nil {
			return nil, ctxerr.Wrapf(ctx, err, "create activity %s", act.ActivityName())
		}
	}

	if appConfig.MDM.WindowsEnabledAndConfigured && oldAppConfig.MDM.WindowsMigrationEnabled != appConfig.MDM.WindowsMigrationEnabled {
		var act fleet.ActivityDetails
		if appConfig.MDM.WindowsMigrationEnabled {
			act = fleet.ActivityTypeEnabledWindowsMDMMigration{}
		} else {
			act = fleet.ActivityTypeDisabledWindowsMDMMigration{}
		}
		if err := svc.NewActivity(ctx, authz.UserFromContext(ctx), act); err != nil {
			return nil, ctxerr.Wrapf(ctx, err, "create activity %s", act.ActivityName())
		}
	}

	// Create activity if conditional access was enabled or disabled for "No team".
	if conditionalAccessNoTeamUpdated {
		if appConfig.Integrations.ConditionalAccessEnabled.Value {
			if err := svc.NewActivity(
				ctx,
				authz.UserFromContext(ctx),
				fleet.ActivityTypeEnabledConditionalAccessAutomations{
					TeamID:   nil,
					TeamName: "",
				},
			); err != nil {
				return nil, ctxerr.Wrap(ctx, err, "create activity for enabling conditional access")
			}
		} else {
			if err := svc.NewActivity(
				ctx,
				authz.UserFromContext(ctx),
				fleet.ActivityTypeDisabledConditionalAccessAutomations{
					TeamID:   nil,
					TeamName: "",
				},
			); err != nil {
				return nil, ctxerr.Wrap(ctx, err, "create activity for disabling conditional access")
			}
		}
	}

	return obfuscatedAppConfig, nil
}

func (svc *Service) processAppConfigCAs(ctx context.Context, newAppConfig *fleet.AppConfig, oldAppConfig *fleet.AppConfig,
	appConfig *fleet.AppConfig, invalid *fleet.InvalidArgumentError,
) (appConfigCAStatus, error) {
	var invalidLicense bool
	fleetLicense, _ := license.FromContext(ctx)
	if newAppConfig.Integrations.NDESSCEPProxy.Set && newAppConfig.Integrations.NDESSCEPProxy.Valid && !fleetLicense.IsPremium() {
		invalid.Append("integrations.ndes_scep_proxy", ErrMissingLicense.Error())
		appConfig.Integrations.NDESSCEPProxy.Valid = false
		invalidLicense = true
	}
	if newAppConfig.Integrations.DigiCert.Set && newAppConfig.Integrations.DigiCert.Valid && !fleetLicense.IsPremium() {
		invalid.Append("integrations.digicert", ErrMissingLicense.Error())
		appConfig.Integrations.DigiCert.Valid = false
		invalidLicense = true
	}
	if newAppConfig.Integrations.CustomSCEPProxy.Set && newAppConfig.Integrations.CustomSCEPProxy.Valid && !fleetLicense.IsPremium() {
		invalid.Append("integrations.custom_scep_proxy", ErrMissingLicense.Error())
		appConfig.Integrations.CustomSCEPProxy.Valid = false
		invalidLicense = true
	}
	result := appConfigCAStatus{
		digicert:        make(map[string]caStatusType),
		customSCEPProxy: make(map[string]caStatusType),
	}
	if invalidLicense {
		return result, nil
	}

	// Validate NDES SCEP URLs if they changed. Validation is done in both dry run and normal mode.
	switch {
	case !newAppConfig.Integrations.NDESSCEPProxy.Set:
		// Nothing is set -- keep the old value
		appConfig.Integrations.NDESSCEPProxy = oldAppConfig.Integrations.NDESSCEPProxy
	case !newAppConfig.Integrations.NDESSCEPProxy.Valid:
		// User is explicitly clearing this setting
		appConfig.Integrations.NDESSCEPProxy.Valid = false
		if oldAppConfig.Integrations.NDESSCEPProxy.Valid {
			result.ndes = caStatusDeleted
		}
	default:
		// User is updating the setting
		appConfig.Integrations.NDESSCEPProxy.Value.URL = fleet.Preprocess(newAppConfig.Integrations.NDESSCEPProxy.Value.URL)
		appConfig.Integrations.NDESSCEPProxy.Value.AdminURL = fleet.Preprocess(newAppConfig.Integrations.NDESSCEPProxy.Value.AdminURL)
		appConfig.Integrations.NDESSCEPProxy.Value.Username = fleet.Preprocess(newAppConfig.Integrations.NDESSCEPProxy.Value.Username)
		// do not preprocess password
		if len(svc.config.Server.PrivateKey) == 0 {
			invalid.Append("integrations.ndes_scep_proxy",
				"Cannot encrypt NDES password. Missing required private key. Learn how to configure the private key here: https://fleetdm.com/learn-more-about/fleet-server-private-key")
		}

		validateAdminURL, validateURL := false, false
		newSCEPProxy := appConfig.Integrations.NDESSCEPProxy.Value
		if !oldAppConfig.Integrations.NDESSCEPProxy.Valid {
			result.ndes = caStatusAdded
			validateAdminURL, validateURL = true, true
		} else {
			oldSCEPProxy := oldAppConfig.Integrations.NDESSCEPProxy.Value
			if newSCEPProxy.URL != oldSCEPProxy.URL {
				result.ndes = caStatusEdited
				validateURL = true
			}
			if newSCEPProxy.AdminURL != oldSCEPProxy.AdminURL ||
				newSCEPProxy.Username != oldSCEPProxy.Username ||
				(newSCEPProxy.Password != "" && newSCEPProxy.Password != fleet.MaskedPassword) {
				result.ndes = caStatusEdited
				validateAdminURL = true
			}
		}

		if validateAdminURL {
			if err := svc.scepConfigService.ValidateNDESSCEPAdminURL(ctx, newSCEPProxy); err != nil {
				invalid.Append("integrations.ndes_scep_proxy", err.Error())
			}
		}

		if validateURL {
			if err := svc.scepConfigService.ValidateSCEPURL(ctx, newSCEPProxy.URL); err != nil {
				invalid.Append("integrations.ndes_scep_proxy.url", err.Error())
			}
		}
	}

	var (
		allCANames                         = make(map[string]struct{})
		invalidCANames                     = make(map[string]struct{})
		additionalDigiCertValidationNeeded bool
	)

	switch {
	case !newAppConfig.Integrations.DigiCert.Set:
		// Nothing to set -- keep the old value
		appConfig.Integrations.DigiCert = oldAppConfig.Integrations.DigiCert
		// Populate allCANames so we can check for uniqueness against custom SCEP proxy names
		for _, ca := range oldAppConfig.Integrations.DigiCert.Value {
			allCANames[ca.Name] = struct{}{}
		}
	case !newAppConfig.Integrations.DigiCert.Valid || len(newAppConfig.Integrations.DigiCert.Value) == 0:
		// User is explicitly clearing this setting
		appConfig.Integrations.DigiCert.Valid = false
		for _, ca := range oldAppConfig.Integrations.DigiCert.Value {
			result.digicert[ca.Name] = caStatusDeleted
		}
	default:
		// We clear DigiCert CAs because we will repopulate them as we diff old vs new CAs
		appConfig.Integrations.DigiCert.Value = nil
		if len(svc.config.Server.PrivateKey) == 0 {
			invalid.Append("integrations.digicert",
				"Cannot encrypt DigiCert API token. Missing required private key. Learn how to configure the private key here: https://fleetdm."+
					"com/learn-more-about/fleet-server-private-key")
			break
		}
		additionalDigiCertValidationNeeded = true
		for _, ca := range newAppConfig.Integrations.DigiCert.Value {
			ca.Name = fleet.Preprocess(ca.Name)
			if !validateCAName(ca.Name, "digicert", allCANames, invalid) ||
				!validateCACN(ca.CertificateCommonName, invalid) ||
				!validateSeatID(ca.CertificateSeatID, invalid) ||
				!validateUserPrincipalNames(ca.CertificateUserPrincipalNames, invalid) {
				invalidCANames[ca.Name] = struct{}{}
				additionalDigiCertValidationNeeded = false
				continue
			}
			// Validate URL
			ca.URL = fleet.Preprocess(ca.URL)
			if u, err := url.ParseRequestURI(ca.URL); err != nil {
				invalid.Append("integrations.digicert.url", err.Error())
				additionalDigiCertValidationNeeded = false
				continue
			} else if u.Scheme != "https" && u.Scheme != "http" {
				invalid.Append("integrations.digicert.url", "digicert URL must be https or http")
				additionalDigiCertValidationNeeded = false
				continue
			}

			ca.ProfileID = fleet.Preprocess(ca.ProfileID)
			appConfig.Integrations.DigiCert.Value = append(appConfig.Integrations.DigiCert.Value, ca)
		}
	}

	// if additional DigiCert validation is needed, get the encrypted config assets from DB
	if additionalDigiCertValidationNeeded {
		remainingOldCAs := filterDeletedDigiCertCAs(oldAppConfig, newAppConfig, &result)

		err := svc.populateDigiCertAPITokens(ctx, remainingOldCAs)
		if err != nil {
			return result, ctxerr.Wrap(ctx, err, "populate API tokens")
		}
		for i, newCA := range appConfig.Integrations.DigiCert.Value {
			var found, needToVerify bool
			for _, oldCA := range remainingOldCAs {
				switch {
				case newCA.Equals(&oldCA):
					// we clear the APIToken since we don't need to encrypt/save it
					appConfig.Integrations.DigiCert.Value[i].APIToken = fleet.MaskedPassword
					found = true
				case newCA.Name == oldCA.Name:
					// changed
					found = true
					needToVerify = newCA.NeedToVerify(&oldCA)
					if needToVerify && (len(newCA.APIToken) == 0 || newCA.APIToken == fleet.MaskedPassword) {
						invalid.Append("integrations.digicert.api_token",
							fmt.Sprintf("DigiCert API token must be set when modifying name, URL, or GUID of an existing CA: %s", newCA.Name))
						break
					}
					result.digicert[newCA.Name] = caStatusEdited
				}
			}
			if !found {
				if len(newCA.APIToken) == 0 || newCA.APIToken == fleet.MaskedPassword {
					invalid.Append("integrations.digicert.api_token",
						fmt.Sprintf("DigiCert API token must be set on CA: %s", newCA.Name))
					break
				}
				result.digicert[newCA.Name] = caStatusAdded
				needToVerify = true
			}
			if _, ok := result.digicert[newCA.Name]; ok && needToVerify {
				err := svc.digiCertService.VerifyProfileID(ctx, newCA)
				if err != nil {
					invalid.Append("integrations.digicert.profile_id",
						fmt.Sprintf("Could not verify DigiCert profile ID %s for CA %s: %s", newCA.ProfileID, newCA.Name, err))
				}
			}
		}
	}

	var additionalCustomSCEPValidationNeeded bool
	switch {
	case !newAppConfig.Integrations.CustomSCEPProxy.Set:
		// Nothing to set -- keep the old value
		appConfig.Integrations.CustomSCEPProxy = oldAppConfig.Integrations.CustomSCEPProxy
		for _, ca := range oldAppConfig.Integrations.CustomSCEPProxy.Value {
			if _, ok := allCANames[ca.Name]; ok {
				// This issue is caused by the new DigiCert CA added above
				invalid.Append("integrations.digicert.name", fmt.Sprintf("Couldn’t edit certificate authority. "+
					"\"%s\" name is already used by another DigiCert certificate authority. Please choose a different name and try again.", ca.Name))
				additionalCustomSCEPValidationNeeded = false
				continue
			}
			allCANames[ca.Name] = struct{}{}
		}
	case !newAppConfig.Integrations.CustomSCEPProxy.Valid || len(newAppConfig.Integrations.CustomSCEPProxy.Value) == 0:
		// User is explicitly clearing this setting
		appConfig.Integrations.CustomSCEPProxy.Valid = false
		for _, ca := range oldAppConfig.Integrations.CustomSCEPProxy.Value {
			result.customSCEPProxy[ca.Name] = caStatusDeleted
		}
	default:
		// We clear custom SCEP CAs because we will repopulate them as we diff old vs new CAs
		appConfig.Integrations.CustomSCEPProxy.Value = nil
		if len(svc.config.Server.PrivateKey) == 0 {
			invalid.Append("integrations.custom_scep_proxy",
				"Cannot encrypt SCEP challenge. Missing required private key. Learn how to configure the private key here: "+
					"https://fleetdm.com/learn-more-about/fleet-server-private-key")
			break
		}
		additionalCustomSCEPValidationNeeded = true
		for _, ca := range newAppConfig.Integrations.CustomSCEPProxy.Value {
			ca.Name = fleet.Preprocess(ca.Name)
			if !validateCAName(ca.Name, "custom_scep_proxy", allCANames, invalid) {
				invalidCANames[ca.Name] = struct{}{}
				additionalCustomSCEPValidationNeeded = false
				continue
			}
			ca.URL = fleet.Preprocess(ca.URL)
			// Validate URL
			if u, err := url.ParseRequestURI(ca.URL); err != nil {
				invalid.Append("integrations.custom_scep_proxy.url", err.Error())
				additionalCustomSCEPValidationNeeded = false
				continue
			} else if u.Scheme != "https" && u.Scheme != "http" {
				invalid.Append("integrations.custom_scep_proxy.url", "custom_scep_proxy URL must be https or http")
				additionalCustomSCEPValidationNeeded = false
				continue
			}
			appConfig.Integrations.CustomSCEPProxy.Value = append(appConfig.Integrations.CustomSCEPProxy.Value, ca)
		}
	}

	if additionalCustomSCEPValidationNeeded {
		remainingOldCAs := filterDeletedCustomSCEPCAs(oldAppConfig, newAppConfig, &result)
		err := svc.populateCustomSCEPChallenges(ctx, remainingOldCAs)
		if err != nil {
			return result, ctxerr.Wrap(ctx, err, "populate challenges")
		}
		for i, newCA := range appConfig.Integrations.CustomSCEPProxy.Value {
			var found bool
			for _, oldCA := range remainingOldCAs {
				switch {
				case newCA.Equals(&oldCA):
					// we clear the Challenge since we don't need to encrypt/save it
					appConfig.Integrations.CustomSCEPProxy.Value[i].Challenge = fleet.MaskedPassword
					found = true
				case newCA.Name == oldCA.Name:
					// changed
					if len(newCA.Challenge) == 0 || newCA.Challenge == fleet.MaskedPassword {
						invalid.Append("integrations.custom_scep_proxy.challenge",
							fmt.Sprintf("Custom SCEP challenge must be set when modifying existing CA: %s", newCA.Name))
					} else {
						result.customSCEPProxy[newCA.Name] = caStatusEdited
					}
					found = true
				}
			}
			if !found {
				if len(newCA.Challenge) == 0 || newCA.Challenge == fleet.MaskedPassword {
					invalid.Append("integrations.custom_scep_proxy.challenge",
						fmt.Sprintf("Custom SCEP challenge must be set on CA: %s", newCA.Name))
				} else {
					result.customSCEPProxy[newCA.Name] = caStatusAdded
				}
			}
			// Unlike DigiCert, we always validate the connection on add/edit of custom SCEP
			if status, ok := result.customSCEPProxy[newCA.Name]; ok && (status == caStatusEdited || status == caStatusAdded) {
				if err := svc.scepConfigService.ValidateSCEPURL(ctx, newCA.URL); err != nil {
					invalidCANames[newCA.Name] = struct{}{}
					invalid.Append("integrations.custom_scep_proxy.url", err.Error())
				}
			}
		}
	}

	// Remove status updates from invalid CA names
	for caName := range invalidCANames {
		delete(result.digicert, caName)
		delete(result.customSCEPProxy, caName)
	}

	return result, nil
}

func (svc *Service) populateDigiCertAPITokens(ctx context.Context, remainingOldCAs []fleet.DigiCertIntegration) error {
	assets, err := svc.ds.GetAllCAConfigAssetsByType(ctx, fleet.CAConfigDigiCert)
	if err != nil && !fleet.IsNotFound(err) {
		return ctxerr.Wrap(ctx, err, "get DigiCert CA config assets")
	}
	// Note: The added/updated assets will be saved to DB in ds.SaveAppConfig method
	for i, ca := range remainingOldCAs {
		asset, ok := assets[ca.Name]
		if !ok {
			continue
		}
		remainingOldCAs[i].APIToken = string(asset.Value)
	}
	return nil
}

func (svc *Service) populateCustomSCEPChallenges(ctx context.Context, remainingOldCAs []fleet.CustomSCEPProxyIntegration) error {
	assets, err := svc.ds.GetAllCAConfigAssetsByType(ctx, fleet.CAConfigCustomSCEPProxy)
	if err != nil && !fleet.IsNotFound(err) {
		return ctxerr.Wrap(ctx, err, "get custom SCEP CA config assets")
	}
	// Note: The added/updated assets will be saved to DB in ds.SaveAppConfig method
	for i, ca := range remainingOldCAs {
		asset, ok := assets[ca.Name]
		if !ok {
			continue
		}
		remainingOldCAs[i].Challenge = string(asset.Value)
	}
	return nil
}

// filterDeletedDigiCertCAs identifies deleted DigiCert integrations in the provided configs.
// It mutates the provided result to set a deleted status where applicable and returns a list of the remaining (non-deleted) integrations.
func filterDeletedDigiCertCAs(oldAppConfig *fleet.AppConfig, newAppConfig *fleet.AppConfig,
	result *appConfigCAStatus,
) []fleet.DigiCertIntegration {
	remainingOldCAs := make([]fleet.DigiCertIntegration, 0, len(oldAppConfig.Integrations.DigiCert.Value))
	for _, oldCA := range oldAppConfig.Integrations.DigiCert.Value {
		var found bool
		for _, newCA := range newAppConfig.Integrations.DigiCert.Value {
			if oldCA.Name == newCA.Name {
				found = true
				break
			}
		}
		if !found {
			result.digicert[oldCA.Name] = caStatusDeleted
		} else {
			remainingOldCAs = append(remainingOldCAs, oldCA)
		}
	}
	return remainingOldCAs
}

// filterDeletedCustomSCEPCAs identifies deleted custom SCEP integrations in the provided configs.
// It mutates the provided result to set a deleted status where applicable and returns a list of the remaining (non-deleted) integrations.
func filterDeletedCustomSCEPCAs(oldAppConfig *fleet.AppConfig, newAppConfig *fleet.AppConfig,
	result *appConfigCAStatus,
) []fleet.CustomSCEPProxyIntegration {
	remainingOldCAs := make([]fleet.CustomSCEPProxyIntegration, 0, len(oldAppConfig.Integrations.CustomSCEPProxy.Value))
	for _, oldCA := range oldAppConfig.Integrations.CustomSCEPProxy.Value {
		var found bool
		for _, newCA := range newAppConfig.Integrations.CustomSCEPProxy.Value {
			if oldCA.Name == newCA.Name {
				found = true
				break
			}
		}
		if !found {
			result.customSCEPProxy[oldCA.Name] = caStatusDeleted
		} else {
			remainingOldCAs = append(remainingOldCAs, oldCA)
		}
	}
	return remainingOldCAs
}

func validateCAName(name string, caType string, allCANames map[string]struct{}, invalid *fleet.InvalidArgumentError) bool {
	if name == "NDES" {
		invalid.Append("integrations."+caType+".name", "CA name cannot be NDES")
		return false
	}
	if len(name) == 0 {
		invalid.Append("integrations."+caType+".name", "CA name cannot be empty")
		return false
	}
	if len(name) > 255 {
		invalid.Append("integrations."+caType+".name", "CA name cannot be longer than 255 characters")
		return false
	}
	if !isAlphanumeric(name) {
		invalid.Append("integrations."+caType+".name",
			fmt.Sprintf("Couldn’t edit integrations.%s. Invalid characters in the \"name\" field. Only letters, "+
				"numbers and underscores allowed. %s",
				caType, name))
		return false
	}
	if _, ok := allCANames[name]; ok {
		invalid.Append("integrations."+caType+".name", fmt.Sprintf("Couldn’t edit certificate authority. "+
			"\"%s\" name is already used by another certificate authority. Please choose a different name and try again.", name))
		return false
	}
	allCANames[name] = struct{}{}
	return true
}

func validateCACN(cn string, invalid *fleet.InvalidArgumentError) bool {
	if len(strings.TrimSpace(cn)) == 0 {
		invalid.Append("integrations.digicert.certificate_common_name", "CA Common Name (CN) cannot be empty")
		return false
	}
	fleetVars := findFleetVariables(cn)
	for fleetVar := range fleetVars {
		switch fleetVar {
		case fleet.FleetVarHostEndUserEmailIDP, fleet.FleetVarHostHardwareSerial:
			// ok
		default:
			invalid.Append("integrations.digicert.certificate_common_name", "FLEET_VAR_"+fleetVar+" is not allowed in CA Common Name (CN)")
			return false
		}
	}
	return true
}

func validateSeatID(seatID string, invalid *fleet.InvalidArgumentError) bool {
	if len(strings.TrimSpace(seatID)) == 0 {
		invalid.Append("integrations.digicert.certificate_seat_id", "CA Seat ID cannot be empty")
		return false
	}
	fleetVars := findFleetVariables(seatID)
	for fleetVar := range fleetVars {
		switch fleetVar {
		case fleet.FleetVarHostEndUserEmailIDP, fleet.FleetVarHostHardwareSerial:
			// ok
		default:
			invalid.Append("integrations.digicert.certificate_seat_id", "FLEET_VAR_"+fleetVar+" is not allowed in DigiCert Seat ID")
			return false
		}
	}
	return true
}

func validateUserPrincipalNames(userPrincipalNames []string, invalid *fleet.InvalidArgumentError) bool {
	if len(userPrincipalNames) == 0 {
		return true
	}
	if len(userPrincipalNames) > 1 {
		invalid.Append("integrations.digicert.certificate_user_principal_names",
			"DigiCert CA can only have one certificate user principal name")
		return false
	}
	if len(strings.TrimSpace(userPrincipalNames[0])) == 0 {
		invalid.Append("integrations.digicert.certificate_user_principal_names",
			"DigiCert CA certificate user principal name cannot be empty if specified")
		return false
	}
	fleetVars := findFleetVariables(userPrincipalNames[0])
	for fleetVar := range fleetVars {
		switch fleetVar {
		case fleet.FleetVarHostEndUserEmailIDP, fleet.FleetVarHostHardwareSerial:
			// ok
		default:
			invalid.Append("integrations.digicert.certificate_user_principal_names",
				"FLEET_VAR_"+fleetVar+" is not allowed in CA User Principal Name")
			return false
		}
	}
	return true
}

type appConfigCAStatus struct {
	ndes            caStatusType
	digicert        map[string]caStatusType
	customSCEPProxy map[string]caStatusType
}

type caStatusType string

const (
	caStatusAdded   caStatusType = "added"
	caStatusEdited  caStatusType = "edited"
	caStatusDeleted caStatusType = "deleted"
)

// processAppleOSUpdateSettings updates the OS updates configuration if the minimum version+deadline are updated.
func (svc *Service) processAppleOSUpdateSettings(
	ctx context.Context,
	license *fleet.LicenseInfo,
	appleDevice fleet.AppleDevice,
	oldOSUpdateSettings fleet.AppleOSUpdateSettings,
	newOSUpdateSettings fleet.AppleOSUpdateSettings,
) error {
	if oldOSUpdateSettings.MinimumVersion.Value != newOSUpdateSettings.MinimumVersion.Value ||
		oldOSUpdateSettings.Deadline.Value != newOSUpdateSettings.Deadline.Value {
		if license.IsPremium() {
			if err := svc.EnterpriseOverrides.MDMAppleEditedAppleOSUpdates(ctx, nil, appleDevice, newOSUpdateSettings); err != nil {
				return ctxerr.Wrap(ctx, err, "update DDM profile after Apple OS updates change")
			}
		}

		var activity fleet.ActivityDetails
		switch appleDevice {
		case fleet.MacOS:
			activity = fleet.ActivityTypeEditedMacOSMinVersion{
				MinimumVersion: newOSUpdateSettings.MinimumVersion.Value,
				Deadline:       newOSUpdateSettings.Deadline.Value,
			}
		case fleet.IOS:
			activity = fleet.ActivityTypeEditedIOSMinVersion{
				MinimumVersion: newOSUpdateSettings.MinimumVersion.Value,
				Deadline:       newOSUpdateSettings.Deadline.Value,
			}
		case fleet.IPadOS:
			activity = fleet.ActivityTypeEditedIPadOSMinVersion{
				MinimumVersion: newOSUpdateSettings.MinimumVersion.Value,
				Deadline:       newOSUpdateSettings.Deadline.Value,
			}
		}
		if err := svc.NewActivity(ctx, authz.UserFromContext(ctx), activity); err != nil {
			return ctxerr.Wrap(ctx, err, "create activity for app config apple min version modification")
		}
	}
	return nil
}

func (svc *Service) HasCustomSetupAssistantConfigurationWebURL(ctx context.Context, teamID *uint) (bool, error) {
	az, ok := authz_ctx.FromContext(ctx)
	if !ok || !az.Checked() {
		return false, fleet.NewAuthRequiredError("method requires previous authorization")
	}

	asst, err := svc.ds.GetMDMAppleSetupAssistant(ctx, teamID)
	if err != nil {
		if fleet.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	var m map[string]any
	if err := json.Unmarshal(asst.Profile, &m); err != nil {
		return false, err
	}

	_, ok = m["configuration_web_url"]
	return ok, nil
}

func (svc *Service) validateMDM(
	ctx context.Context,
	license *fleet.LicenseInfo,
	oldMdm *fleet.MDM,
	mdm *fleet.MDM,
	invalid *fleet.InvalidArgumentError,
) error {
	if mdm.EnableDiskEncryption.Value && !license.IsPremium() {
		invalid.Append("macos_settings.enable_disk_encryption", ErrMissingLicense.Error())
	}
	if mdm.MacOSSetup.MacOSSetupAssistant.Value != "" && oldMdm.MacOSSetup.MacOSSetupAssistant.Value != mdm.MacOSSetup.MacOSSetupAssistant.Value && !license.IsPremium() {
		invalid.Append("macos_setup.macos_setup_assistant", ErrMissingLicense.Error())
	}
	if mdm.MacOSSetup.EnableReleaseDeviceManually.Value && oldMdm.MacOSSetup.EnableReleaseDeviceManually.Value != mdm.MacOSSetup.EnableReleaseDeviceManually.Value && !license.IsPremium() {
		invalid.Append("macos_setup.enable_release_device_manually", ErrMissingLicense.Error())
	}
	if mdm.MacOSSetup.BootstrapPackage.Value != "" && oldMdm.MacOSSetup.BootstrapPackage.Value != mdm.MacOSSetup.BootstrapPackage.Value && !license.IsPremium() {
		invalid.Append("macos_setup.bootstrap_package", ErrMissingLicense.Error())
	}
	if mdm.MacOSSetup.EnableEndUserAuthentication && oldMdm.MacOSSetup.EnableEndUserAuthentication != mdm.MacOSSetup.EnableEndUserAuthentication && !license.IsPremium() {
		invalid.Append("macos_setup.enable_end_user_authentication", ErrMissingLicense.Error())
	}
	if mdm.MacOSSetup.ManualAgentInstall.Valid && oldMdm.MacOSSetup.ManualAgentInstall.Value != mdm.MacOSSetup.ManualAgentInstall.Value && !license.IsPremium() {
		invalid.Append("macos_setup.manual_agent_install", ErrMissingLicense.Error())
	}
	if mdm.WindowsMigrationEnabled && !license.IsPremium() {
		invalid.Append("windows_migration_enabled", ErrMissingLicense.Error())
	}

	// we want to use `oldMdm` here as this boolean is set by the fleet
	// server at startup and can't be modified by the user
	if !oldMdm.EnabledAndConfigured {
		if len(mdm.MacOSSettings.CustomSettings) > 0 && !fleet.MDMProfileSpecsMatch(mdm.MacOSSettings.CustomSettings, oldMdm.MacOSSettings.CustomSettings) {
			invalid.Append("macos_settings.custom_settings",
				`Couldn't update macos_settings because MDM features aren't turned on in Fleet. Use fleetctl generate mdm-apple and then fleet serve with mdm configuration to turn on MDM features.`)
		}

		if mdm.MacOSSetup.MacOSSetupAssistant.Value != "" && oldMdm.MacOSSetup.MacOSSetupAssistant.Value != mdm.MacOSSetup.MacOSSetupAssistant.Value {
			invalid.Append("macos_setup.macos_setup_assistant",
				`Couldn't update macos_setup because MDM features aren't turned on in Fleet. Use fleetctl generate mdm-apple and then fleet serve with mdm configuration to turn on MDM features.`)
		}

		if mdm.MacOSSetup.EnableReleaseDeviceManually.Value && oldMdm.MacOSSetup.EnableReleaseDeviceManually.Value != mdm.MacOSSetup.EnableReleaseDeviceManually.Value {
			invalid.Append("macos_setup.enable_release_device_manually",
				`Couldn't update macos_setup because MDM features aren't turned on in Fleet. Use fleetctl generate mdm-apple and then fleet serve with mdm configuration to turn on MDM features.`)
		}

		if mdm.MacOSSetup.BootstrapPackage.Value != "" && oldMdm.MacOSSetup.BootstrapPackage.Value != mdm.MacOSSetup.BootstrapPackage.Value {
			invalid.Append("macos_setup.bootstrap_package",
				`Couldn't update macos_setup because MDM features aren't turned on in Fleet. Use fleetctl generate mdm-apple and then fleet serve with mdm configuration to turn on MDM features.`)
		}
		if mdm.MacOSSetup.EnableEndUserAuthentication && oldMdm.MacOSSetup.EnableEndUserAuthentication != mdm.MacOSSetup.EnableEndUserAuthentication {
			invalid.Append("macos_setup.enable_end_user_authentication",
				`Couldn't update macos_setup because MDM features aren't turned on in Fleet. Use fleetctl generate mdm-apple and then fleet serve with mdm configuration to turn on MDM features.`)
		}
	}
	checkCustomSettings := func(prefix string, customSettings []fleet.MDMProfileSpec) {
		for i, prof := range customSettings {
			count := 0
			for _, b := range []bool{
				len(prof.Labels) > 0,
				len(prof.LabelsIncludeAll) > 0,
				len(prof.LabelsIncludeAny) > 0,
				len(prof.LabelsExcludeAny) > 0,
			} {
				if b {
					count++
				}
			}
			if count > 1 {
				invalid.Append(fmt.Sprintf("%s_settings.custom_settings", prefix),
					fmt.Sprintf(`Couldn't edit %s_settings.custom_settings. For each profile, only one of "labels_exclude_any", "labels_include_all", "labels_include_any" or "labels" can be included.`, prefix))
			}
			if len(prof.Labels) > 0 {
				customSettings[i].LabelsIncludeAll = customSettings[i].Labels
				customSettings[i].Labels = nil
			}
		}
	}
	checkCustomSettings("macos", mdm.MacOSSettings.CustomSettings)

	if !mdm.WindowsEnabledAndConfigured {
		if mdm.WindowsSettings.CustomSettings.Set &&
			len(mdm.WindowsSettings.CustomSettings.Value) > 0 &&
			!fleet.MDMProfileSpecsMatch(mdm.WindowsSettings.CustomSettings.Value, oldMdm.WindowsSettings.CustomSettings.Value) {
			invalid.Append("windows_settings.custom_settings",
				`Couldn’t edit windows_settings.custom_settings. Windows MDM isn’t turned on. This can be enabled by setting "controls.windows_enabled_and_configured: true" in the default configuration. Visit https://fleetdm.com/guides/windows-mdm-setup and https://fleetdm.com/docs/configuration/yaml-files#controls to learn more about enabling MDM.`)
		}
	}
	checkCustomSettings("windows", mdm.WindowsSettings.CustomSettings.Value)

	// MacOSUpdates
	updatingMacOSVersion := mdm.MacOSUpdates.MinimumVersion.Value != "" &&
		mdm.MacOSUpdates.MinimumVersion != oldMdm.MacOSUpdates.MinimumVersion
	updatingMacOSDeadline := mdm.MacOSUpdates.Deadline.Value != "" &&
		mdm.MacOSUpdates.Deadline != oldMdm.MacOSUpdates.Deadline
	// IOSUpdates
	updatingIOSVersion := mdm.IOSUpdates.MinimumVersion.Value != "" &&
		mdm.IOSUpdates.MinimumVersion != oldMdm.IOSUpdates.MinimumVersion
	updatingIOSDeadline := mdm.IOSUpdates.Deadline.Value != "" &&
		mdm.IOSUpdates.Deadline != oldMdm.IOSUpdates.Deadline
	// IPadOSUpdates
	updatingIPadOSVersion := mdm.IPadOSUpdates.MinimumVersion.Value != "" &&
		mdm.IPadOSUpdates.MinimumVersion != oldMdm.IPadOSUpdates.MinimumVersion
	updatingIPadOSDeadline := mdm.IPadOSUpdates.Deadline.Value != "" &&
		mdm.IPadOSUpdates.Deadline != oldMdm.IPadOSUpdates.Deadline

	if updatingMacOSVersion || updatingMacOSDeadline ||
		updatingIOSVersion || updatingIOSDeadline ||
		updatingIPadOSVersion || updatingIPadOSDeadline {
		// TODO: Should we validate MDM configured on here too?

		if !license.IsPremium() {
			invalid.Append("macos_updates.minimum_version", ErrMissingLicense.Error())
			return nil
		}
	}
	if err := mdm.MacOSUpdates.Validate(); err != nil {
		invalid.Append("macos_updates", err.Error())
	}
	if err := mdm.IOSUpdates.Validate(); err != nil {
		invalid.Append("ios_updates", err.Error())
	}
	if err := mdm.IPadOSUpdates.Validate(); err != nil {
		invalid.Append("ipados_updates", err.Error())
	}

	// WindowsUpdates
	updatingWindowsUpdates := !mdm.WindowsUpdates.Equal(oldMdm.WindowsUpdates)
	if updatingWindowsUpdates {
		// TODO: Should we validate MDM configured on here too?

		if !license.IsPremium() {
			invalid.Append("windows_updates.deadline_days", ErrMissingLicense.Error())
			return nil
		}
	}
	if err := mdm.WindowsUpdates.Validate(); err != nil {
		invalid.Append("windows_updates", err.Error())
	}

	// EndUserAuthentication
	// only validate SSO settings if they changed
	if mdm.EndUserAuthentication.SSOProviderSettings != oldMdm.EndUserAuthentication.SSOProviderSettings {
		if !license.IsPremium() {
			invalid.Append("end_user_authentication", ErrMissingLicense.Error())
			return nil
		}
		validateSSOProviderSettings(mdm.EndUserAuthentication.SSOProviderSettings, oldMdm.EndUserAuthentication.SSOProviderSettings, invalid)
	}

	// MacOSSetup validation
	if mdm.MacOSSetup.EnableEndUserAuthentication {
		if mdm.EndUserAuthentication.IsEmpty() {
			// TODO: update this error message to include steps to resolve the issue once docs for IdP
			// config are available
			invalid.Append("macos_setup.enable_end_user_authentication",
				`Couldn't enable macos_setup.enable_end_user_authentication because no IdP is configured for MDM features.`)
		}
	}

	if mdm.MacOSSetup.EnableEndUserAuthentication != oldMdm.MacOSSetup.EnableEndUserAuthentication {
		hasCustomConfigurationWebURL, err := svc.HasCustomSetupAssistantConfigurationWebURL(ctx, nil)
		if err != nil {
			return ctxerr.Wrap(ctx, err, "checking setup assistant configuration web url")
		}
		if hasCustomConfigurationWebURL {
			invalid.Append("end_user_authentication", fleet.EndUserAuthDEPWebURLConfiguredErrMsg)
		}
	}

	updatingMacOSMigration := mdm.MacOSMigration.Enable != oldMdm.MacOSMigration.Enable ||
		mdm.MacOSMigration.Mode != oldMdm.MacOSMigration.Mode ||
		mdm.MacOSMigration.WebhookURL != oldMdm.MacOSMigration.WebhookURL

	// MacOSMigration validation
	if updatingMacOSMigration {
		// TODO: Should we validate MDM configured on here too?

		if mdm.MacOSMigration.Enable {
			if !license.IsPremium() {
				invalid.Append("macos_migration.enable", ErrMissingLicense.Error())
				return nil
			}
			if !mdm.MacOSMigration.Mode.IsValid() {
				invalid.Append("macos_migration.mode", "mode must be one of 'voluntary' or 'forced'")
			}
			// TODO: improve url validation generally
			if u, err := url.ParseRequestURI(mdm.MacOSMigration.WebhookURL); err != nil {
				invalid.Append("macos_migration.webhook_url", err.Error())
			} else if u.Scheme != "https" && u.Scheme != "http" {
				invalid.Append("macos_migration.webhook_url", "webhook_url must be https or http")
			}
		}
	}

	// Windows validation
	if !svc.config.MDM.IsMicrosoftWSTEPSet() {
		if mdm.WindowsEnabledAndConfigured {
			invalid.Append("mdm.windows_enabled_and_configured", "Couldn't turn on Windows MDM. Please configure Fleet with a certificate and key pair first.")
			return nil
		}
	}
	if !mdm.WindowsEnabledAndConfigured && mdm.WindowsMigrationEnabled {
		invalid.Append("mdm.windows_migration_enabled", "Couldn't enable Windows MDM migration, Windows MDM is not enabled.")
	}
	return nil
}

func (svc *Service) validateABMAssignments(
	ctx context.Context,
	mdm, oldMdm *fleet.MDM,
	invalid *fleet.InvalidArgumentError,
	license *fleet.LicenseInfo,
) ([]*fleet.ABMToken, error) {
	if mdm.DeprecatedAppleBMDefaultTeam != "" && mdm.AppleBusinessManager.Set && mdm.AppleBusinessManager.Valid {
		invalid.Append("mdm.apple_bm_default_team", fleet.AppleABMDefaultTeamDeprecatedMessage)
		return nil, nil
	}

	if name := mdm.DeprecatedAppleBMDefaultTeam; name != "" && name != oldMdm.DeprecatedAppleBMDefaultTeam {
		if !license.IsPremium() {
			invalid.Append("mdm.apple_bm_default_team", ErrMissingLicense.Error())
			return nil, nil
		}
		team, err := svc.ds.TeamByName(ctx, name)
		if err != nil {
			invalid.Append("mdm.apple_bm_default_team", "team name not found")
			return nil, nil
		}
		tokens, err := svc.ds.ListABMTokens(ctx)
		if err != nil {
			return nil, err
		}

		if len(tokens) > 1 {
			invalid.Append("mdm.apple_bm_default_team", fleet.AppleABMDefaultTeamDeprecatedMessage)
			return nil, nil
		}

		if len(tokens) == 0 {
			invalid.Append("mdm.apple_bm_default_team", "no ABM tokens found")
			return nil, nil
		}

		tok := tokens[0]
		tok.MacOSDefaultTeamID = &team.ID
		tok.IOSDefaultTeamID = &team.ID
		tok.IPadOSDefaultTeamID = &team.ID

		return []*fleet.ABMToken{tok}, nil
	}

	if mdm.AppleBusinessManager.Set && len(mdm.AppleBusinessManager.Value) > 0 {
		if !license.IsPremium() {
			invalid.Append("mdm.apple_business_manager", ErrMissingLicense.Error())
			return nil, nil
		}

		teams, err := svc.ds.TeamsSummary(ctx)
		if err != nil {
			return nil, err
		}
		teamsByName := map[string]*uint{"": nil, "No team": nil}
		for _, tm := range teams {
			teamsByName[tm.Name] = &tm.ID
		}
		tokens, err := svc.ds.ListABMTokens(ctx)
		if err != nil {
			return nil, err
		}
		tokensByName := map[string]*fleet.ABMToken{}
		for _, token := range tokens {
			// The default assignments for all tokens is "no team"
			// (ie: team_id IS NULL), here we reset the assignments
			// for all tokens, those will be re-added below.
			//
			// This ensures any unassignments are properly handled.
			token.MacOSDefaultTeamID = nil
			token.IOSDefaultTeamID = nil
			token.IPadOSDefaultTeamID = nil
			tokensByName[token.OrganizationName] = token
		}

		var tokensToSave []*fleet.ABMToken
		for _, bm := range mdm.AppleBusinessManager.Value {
			for _, tmName := range []string{bm.MacOSTeam, bm.IOSTeam, bm.IpadOSTeam} {
				if _, ok := teamsByName[norm.NFC.String(tmName)]; !ok {
					invalid.Appendf("mdm.apple_business_manager", "team %s doesn't exist", tmName)
					return nil, nil
				}
			}

			if _, ok := tokensByName[norm.NFC.String(bm.OrganizationName)]; !ok {
				invalid.Appendf("mdm.apple_business_manager", "token with organization name %s doesn't exist", bm.OrganizationName)
				return nil, nil
			}

			tok := tokensByName[bm.OrganizationName]
			tok.MacOSDefaultTeamID = teamsByName[bm.MacOSTeam]
			tok.IOSDefaultTeamID = teamsByName[bm.IOSTeam]
			tok.IPadOSDefaultTeamID = teamsByName[bm.IpadOSTeam]
			tokensToSave = append(tokensToSave, tok)
		}

		return tokensToSave, nil
	}

	return nil, nil
}

func (svc *Service) validateVPPAssignments(
	ctx context.Context,
	volumePurchasingProgramInfo []fleet.MDMAppleVolumePurchasingProgramInfo,
	invalid *fleet.InvalidArgumentError,
	license *fleet.LicenseInfo,
) (map[uint][]uint, error) {
	// Allow clearing VPP assignments in free and premium.
	if len(volumePurchasingProgramInfo) == 0 {
		return nil, nil
	}

	if !license.IsPremium() {
		invalid.Append("mdm.volume_purchasing_program", ErrMissingLicense.Error())
		return nil, nil
	}

	teams, err := svc.ds.TeamsSummary(ctx)
	if err != nil {
		return nil, err
	}
	teamsByName := map[string]uint{fleet.TeamNameNoTeam: 0}
	for _, tm := range teams {
		teamsByName[tm.Name] = tm.ID
	}
	tokens, err := svc.ds.ListVPPTokens(ctx)
	if err != nil {
		return nil, err
	}
	tokensByLocation := map[string]*fleet.VPPTokenDB{}
	for _, token := range tokens {
		// The default assignments for all tokens is "no team"
		// (ie: team_id IS NULL), here we reset the assignments
		// for all tokens, those will be re-added below.
		//
		// This ensures any unassignments are properly handled.
		tokensByLocation[token.Location] = token
		token.Teams = nil
	}

	tokensToSave := make(map[uint][]uint, len(volumePurchasingProgramInfo))
	for _, vpp := range volumePurchasingProgramInfo {
		for _, tmName := range vpp.Teams {
			if _, ok := teamsByName[norm.NFC.String(tmName)]; !ok && tmName != fleet.TeamNameAllTeams {
				invalid.Appendf("mdm.volume_purchasing_program", "team %s doesn't exist", tmName)
				return nil, nil
			}
		}

		loc := norm.NFC.String(vpp.Location)
		if _, ok := tokensByLocation[loc]; !ok {
			invalid.Appendf("mdm.volume_purchasing_program", "token with location %s doesn't exist", vpp.Location)
			return nil, nil
		}

		var tokenTeams []uint
		for _, teamName := range vpp.Teams {
			if teamName == fleet.TeamNameAllTeams {
				if len(vpp.Teams) > 1 {
					invalid.Appendf("mdm.volume_purchasing_program", "token cannot belong to %s and other teams", fleet.TeamNameAllTeams)
					return nil, nil
				}
				tokenTeams = []uint{}
				break
			}
			teamID := teamsByName[teamName]
			tokenTeams = append(tokenTeams, teamID)
		}

		tok := tokensByLocation[loc]
		tokensToSave[tok.ID] = tokenTeams
	}

	return tokensToSave, nil
}

func validateSSOProviderSettings(incoming, existing fleet.SSOProviderSettings, invalid *fleet.InvalidArgumentError) {
	if incoming.Metadata == "" && incoming.MetadataURL == "" {
		if existing.Metadata == "" && existing.MetadataURL == "" {
			invalid.Append("metadata", "either metadata or metadata_url must be defined")
		}
	}
	if incoming.EntityID == "" {
		if existing.EntityID == "" {
			invalid.Append("entity_id", "required")
		}
	} else if len(incoming.EntityID) < 5 {
		invalid.Append("entity_id", "must be 5 or more characters")
	}
	if incoming.IDPName == "" {
		if existing.IDPName == "" {
			invalid.Append("idp_name", "required")
		}
	}

	if incoming.MetadataURL != "" {
		if u, err := url.ParseRequestURI(incoming.MetadataURL); err != nil {
			invalid.Append("metadata_url", err.Error())
		} else if u.Scheme != "https" && u.Scheme != "http" {
			invalid.Append("metadata_url", "must be either https or http")
		}
	}
}

func validateSSOSettings(p fleet.AppConfig, existing *fleet.AppConfig, invalid *fleet.InvalidArgumentError, license *fleet.LicenseInfo) {
	if p.SSOSettings != nil && p.SSOSettings.EnableSSO {

		var existingSSOProviderSettings fleet.SSOProviderSettings
		if existing.SSOSettings != nil {
			existingSSOProviderSettings = existing.SSOSettings.SSOProviderSettings
		}
		validateSSOProviderSettings(p.SSOSettings.SSOProviderSettings, existingSSOProviderSettings, invalid)

		if !license.IsPremium() {
			if p.SSOSettings.EnableJITProvisioning {
				invalid.Append("enable_jit_provisioning", ErrMissingLicense.Error())
			}
		}
	}
}

// //////////////////////////////////////////////////////////////////////////////
// Apply enroll secret spec
// //////////////////////////////////////////////////////////////////////////////

type applyEnrollSecretSpecRequest struct {
	Spec   *fleet.EnrollSecretSpec `json:"spec"`
	DryRun bool                    `json:"-" query:"dry_run,optional"` // if true, apply validation but do not save changes
}

type applyEnrollSecretSpecResponse struct {
	Err error `json:"error,omitempty"`
}

func (r applyEnrollSecretSpecResponse) Error() error { return r.Err }

func applyEnrollSecretSpecEndpoint(ctx context.Context, request interface{}, svc fleet.Service) (fleet.Errorer, error) {
	req := request.(*applyEnrollSecretSpecRequest)
	err := svc.ApplyEnrollSecretSpec(
		ctx, req.Spec, fleet.ApplySpecOptions{
			DryRun: req.DryRun,
		},
	)
	if err != nil {
		return applyEnrollSecretSpecResponse{Err: err}, nil
	}
	return applyEnrollSecretSpecResponse{}, nil
}

func (svc *Service) ApplyEnrollSecretSpec(ctx context.Context, spec *fleet.EnrollSecretSpec, applyOpts fleet.ApplySpecOptions) error {
	if err := svc.authz.Authorize(ctx, &fleet.EnrollSecret{}, fleet.ActionWrite); err != nil {
		return err
	}
	if len(spec.Secrets) > fleet.MaxEnrollSecretsCount {
		return ctxerr.Wrap(ctx, fleet.NewInvalidArgumentError("secrets", "too many secrets"))
	}

	for _, s := range spec.Secrets {
		if s.Secret == "" {
			return ctxerr.New(ctx, "enroll secret must not be empty")
		}
	}

	if applyOpts.DryRun {
		for _, s := range spec.Secrets {
			available, err := svc.ds.IsEnrollSecretAvailable(ctx, s.Secret, false, nil)
			if err != nil {
				return err
			}
			if !available {
				return ctxerr.Wrap(ctx, fleet.NewInvalidArgumentError("secrets", "a provided global enroll secret is already being used"))
			}
		}
		return nil
	}

	return svc.ds.ApplyEnrollSecrets(ctx, nil, spec.Secrets)
}

// //////////////////////////////////////////////////////////////////////////////
// Get enroll secret spec
// //////////////////////////////////////////////////////////////////////////////

type getEnrollSecretSpecResponse struct {
	Spec *fleet.EnrollSecretSpec `json:"spec"`
	Err  error                   `json:"error,omitempty"`
}

func (r getEnrollSecretSpecResponse) Error() error { return r.Err }

func getEnrollSecretSpecEndpoint(ctx context.Context, request interface{}, svc fleet.Service) (fleet.Errorer, error) {
	specs, err := svc.GetEnrollSecretSpec(ctx)
	if err != nil {
		return getEnrollSecretSpecResponse{Err: err}, nil
	}
	return getEnrollSecretSpecResponse{Spec: specs}, nil
}

func (svc *Service) GetEnrollSecretSpec(ctx context.Context) (*fleet.EnrollSecretSpec, error) {
	if err := svc.authz.Authorize(ctx, &fleet.EnrollSecret{}, fleet.ActionRead); err != nil {
		return nil, err
	}

	secrets, err := svc.ds.GetEnrollSecrets(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &fleet.EnrollSecretSpec{Secrets: secrets}, nil
}

// //////////////////////////////////////////////////////////////////////////////
// Version
// //////////////////////////////////////////////////////////////////////////////

type versionResponse struct {
	*version.Info
	Err error `json:"error,omitempty"`
}

func (r versionResponse) Error() error { return r.Err }

func versionEndpoint(ctx context.Context, request interface{}, svc fleet.Service) (fleet.Errorer, error) {
	info, err := svc.Version(ctx)
	if err != nil {
		return versionResponse{Err: err}, nil
	}
	return versionResponse{Info: info}, nil
}

func (svc *Service) Version(ctx context.Context) (*version.Info, error) {
	if err := svc.authz.Authorize(ctx, &fleet.Version{}, fleet.ActionRead); err != nil {
		return nil, err
	}

	info := version.Version()
	return &info, nil
}

// //////////////////////////////////////////////////////////////////////////////
// Get Certificate Chain
// //////////////////////////////////////////////////////////////////////////////

type getCertificateResponse struct {
	CertificateChain []byte `json:"certificate_chain"`
	Err              error  `json:"error,omitempty"`
}

func (r getCertificateResponse) Error() error { return r.Err }

func getCertificateEndpoint(ctx context.Context, request interface{}, svc fleet.Service) (fleet.Errorer, error) {
	chain, err := svc.CertificateChain(ctx)
	if err != nil {
		return getCertificateResponse{Err: err}, nil
	}
	return getCertificateResponse{CertificateChain: chain}, nil
}

// Certificate returns the PEM encoded certificate chain for osqueryd TLS termination.
func (svc *Service) CertificateChain(ctx context.Context) ([]byte, error) {
	config, err := svc.AppConfigObfuscated(ctx)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(config.ServerSettings.ServerURL)
	if err != nil {
		return nil, ctxerr.Wrap(ctx, err, "parsing serverURL")
	}

	conn, err := connectTLS(ctx, u)
	if err != nil {
		return nil, err
	}

	return chain(ctx, conn.ConnectionState(), u.Hostname())
}

func connectTLS(ctx context.Context, serverURL *url.URL) (*tls.Conn, error) {
	var hostport string
	if serverURL.Port() == "" {
		hostport = net.JoinHostPort(serverURL.Host, "443")
	} else {
		hostport = serverURL.Host
	}

	// attempt dialing twice, first with a secure conn, and then
	// if that fails, use insecure
	dial := func(insecure bool) (*tls.Conn, error) {
		conn, err := tls.Dial("tcp", hostport, &tls.Config{
			InsecureSkipVerify: insecure,
		})
		if err != nil {
			return nil, ctxerr.Wrap(ctx, err, "dial tls")
		}
		defer conn.Close()
		return conn, nil
	}

	var (
		conn *tls.Conn
		err  error
	)

	conn, err = dial(false)
	if err == nil {
		return conn, nil
	}
	conn, err = dial(true)
	return conn, err
}

// chain builds a PEM encoded certificate chain using the PeerCertificates
// in tls.ConnectionState. chain uses the hostname to omit the Leaf certificate
// from the chain.
func chain(ctx context.Context, cs tls.ConnectionState, hostname string) ([]byte, error) {
	buf := bytes.NewBuffer([]byte(""))

	verifyEncode := func(chain []*x509.Certificate) error {
		for _, cert := range chain {
			if len(chain) > 1 {
				// drop the leaf certificate from the chain. osqueryd does not
				// need it to establish a secure connection
				if err := cert.VerifyHostname(hostname); err == nil {
					continue
				}
			}
			if err := encodePEMCertificate(buf, cert); err != nil {
				return err
			}
		}
		return nil
	}

	// use verified chains if available(which adds the root CA), otherwise
	// use the certificate chain offered by the server (if terminated with
	// self-signed certs)
	if len(cs.VerifiedChains) != 0 {
		for _, chain := range cs.VerifiedChains {
			if err := verifyEncode(chain); err != nil {
				return nil, ctxerr.Wrap(ctx, err, "encode verified chains pem")
			}
		}
	} else {
		if err := verifyEncode(cs.PeerCertificates); err != nil {
			return nil, ctxerr.Wrap(ctx, err, "encode peer certificates pem")
		}
	}
	return buf.Bytes(), nil
}

func encodePEMCertificate(buf io.Writer, cert *x509.Certificate) error {
	block := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	}
	return pem.Encode(buf, block)
}

func (svc *Service) HostFeatures(ctx context.Context, host *fleet.Host) (*fleet.Features, error) {
	if svc.EnterpriseOverrides != nil {
		return svc.EnterpriseOverrides.HostFeatures(ctx, host)
	}

	appConfig, err := svc.ds.AppConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &appConfig.Features, nil
}

var alphanumeric = regexp.MustCompile(`^\w+$`)

func isAlphanumeric(s string) bool {
	return alphanumeric.MatchString(s)
}
