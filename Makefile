.PHONY: build clean clean-assets e2e-reset-db e2e-serve e2e-setup changelog db-reset db-backup db-restore check-go-cloner update-go-cloner help

export GO111MODULE=on

PATH := $(shell npm bin):$(PATH)
VERSION = $(shell git describe --tags --always --dirty)
BRANCH = $(shell git rev-parse --abbrev-ref HEAD)
REVISION = $(shell git rev-parse HEAD)
REVSHORT = $(shell git rev-parse --short HEAD)
USER = $(shell whoami)
DOCKER_IMAGE_NAME = fleetdm/fleet
# The tool that was called on the command line (probably `make` or `fdm`).
TOOL_CMD = "make"

ifdef GO_BUILD_RACE_ENABLED
GO_BUILD_RACE_ENABLED_VAR := true
else
GO_BUILD_RACE_ENABLED_VAR := false
endif

ifneq ($(OS), Windows_NT)
	# If on macOS, set the shell to bash explicitly
	ifeq ($(shell uname), Darwin)
		SHELL := /bin/bash
	endif

	# The output binary name is different on Windows, so we're explicit here
	OUTPUT = fleet

	# To populate version metadata, we use unix tools to get certain data
	GOVERSION = $(shell go version | awk '{print $$3}')
	NOW	= $(shell date +"%Y-%m-%d")
else
	# The output binary name is different on Windows, so we're explicit here
	OUTPUT = fleet.exe

	# To populate version metadata, we use windows tools to get the certain data
	GOVERSION_CMD = "(go version).Split()[2]"
	GOVERSION = $(shell powershell $(GOVERSION_CMD))
	NOW	= $(shell powershell Get-Date -format "yyy-MM-dd")
endif

ifndef CIRCLE_PR_NUMBER
	DOCKER_IMAGE_TAG = ${REVSHORT}
else
	DOCKER_IMAGE_TAG = dev-${CIRCLE_PR_NUMBER}-${REVSHORT}
endif

ifdef CIRCLE_TAG
	DOCKER_IMAGE_TAG = ${CIRCLE_TAG}
endif

LDFLAGS_VERSION = "\
	-X github.com/fleetdm/fleet/v4/server/version.appName=${APP_NAME} \
	-X github.com/fleetdm/fleet/v4/server/version.version=${VERSION} \
	-X github.com/fleetdm/fleet/v4/server/version.branch=${BRANCH} \
	-X github.com/fleetdm/fleet/v4/server/version.revision=${REVISION} \
	-X github.com/fleetdm/fleet/v4/server/version.buildDate=${NOW} \
	-X github.com/fleetdm/fleet/v4/server/version.buildUser=${USER} \
	-X github.com/fleetdm/fleet/v4/server/version.goVersion=${GOVERSION}"

# Macro to allow targets to filter out their own arguments from the arguments
# passed to the final command.
# Targets may also add their own CLI arguments to the command as EXTRA_CLI_ARGS.
# See `serve` target for an example.
define filter_args
$(eval FORWARDED_ARGS := $(filter-out $(TARGET_ARGS), $(CLI_ARGS)))
$(eval FORWARDED_ARGS := $(FORWARDED_ARGS) $(EXTRA_CLI_ARGS))
endef

all: build


.prefix:
	mkdir -p build/linux
	mkdir -p build/darwin

.pre-build:
	$(eval GOGC = off)
	$(eval CGO_ENABLED = 0)

.pre-fleet:
	$(eval APP_NAME = fleet)

.pre-fleetctl:
	$(eval APP_NAME = fleetctl)

# For the build target, decide which binaries to build.
# Default to building both
BINS_TO_BUILD = fleet fleetctl
ifeq (build,$(filter build,$(MAKECMDGOALS)))
	BINS_TO_BUILD = fleet fleetctl
	ifeq ($(ARG1), fleet)
		BINS_TO_BUILD = fleet
	else ifeq ($(ARG1), fleetctl)
		BINS_TO_BUILD = fleetctl
	endif
endif
.help-short--build:
	@echo "Build binaries"
.help-long--build:
	@echo "Builds the specified binaries (defaults to building fleet and fleetctl)"
.help-usage--build:
	@echo "$(TOOL_CMD) build [binaries] [options]"
.help-options--build:
	@echo "GO_BUILD_RACE_ENABLED"
	@echo "Turn on data race detection when building"
	@echo "EXTRA_FLEETCTL_LDFLAGS=\"--flag1 --flag2...\""
	@echo "Flags to provide to the Go linker when building fleetctl"
.help-extra--build:
	@echo "AVAILABLE BINARIES:"
	@echo "  fleet      Build the fleet binary"
	@echo "  fleetctl   Build the fleetctl binary"
build: $(BINS_TO_BUILD)

.help-short--fdm:
	@echo "Builds the fdm command"
fdm:
	go build -o build/fdm ./tools/fdm
	@if [ ! -f /usr/local/bin/fdm ]; then \
		echo "Linking to /usr/local/bin/fdm..."; \
		sudo ln -sf "$$(pwd)/build/fdm" /usr/local/bin/fdm; \
	fi

.help-short--serve:
	@echo "Start the fleet server"
.help-short--up:
	@echo "Start the fleet server (alias for \`serve\`)"
.help-long--serve: SERVE_CMD:=serve
.help-long--up: SERVE_CMD:=up
.help-long--serve .help-long--up:
	@echo "Starts an instance of the Fleet web and API server."
	@echo
	@echo "  By default the server will listen on localhost:8080, in development mode with a premium license."
	@echo "  If different options are used to start the server, the options will become 'sticky' and will be used the next time \`$(TOOL_CMD) $(SERVE_CMD)\` is called."
	@echo
	@echo "  To see all available options, run \`$(TOOL_CMD) $(SERVE_CMD) --help\`"
.help-options--serve .help-options--up:
	@echo "HELP"
	@echo "Show all options for the fleet serve command"
	@echo "USE_IP"
	@echo "Start the server on the IP address of the host machine"
	@echo "NO_BUILD"
	@echo "Don't build the fleet binary before starting the server"
	@echo "NO_SAVE"
	@echo "Don't save the current arguments for the next invocation"
	@echo "SHOW"
	@echo "Show the last arguments used to start the server"

up: SERVE_CMD:=up
up: serve
serve: SERVE_CMD:=serve
serve: TARGET_ARGS := --use-ip --no-save --show --no-build
ifdef USE_IP
serve: EXTRA_CLI_ARGS := $(EXTRA_CLI_ARGS) --server_address=$(shell ipconfig getifaddr en0):8080
endif
ifdef SHOW
serve:
	@SAVED_ARGS=$$(cat ~/.fleet/last-serve-invocation); \
	if [[ $$? -eq 0 ]]; then \
		echo "$$SAVED_ARGS"; \
	fi
else ifdef HELP
serve:
	@./build/fleet serve --help
else ifdef RESET
serve:
	@touch ~/.fleet/last-serve-invocation && rm ~/.fleet/last-serve-invocation
else
serve:
	@if [[ "$(NO_BUILD)" != "true" ]]; then make fleet; fi
	$(call filter_args)
# If FORWARDED_ARGS is not empty, run the command with the forwarded arguments.
# Unless NO_SAVE is set to true, save the command to the last invocation file.
# IF FORWARDED_ARGS is empty, attempt to repeat the last invocation.
	@if [[ "$(FORWARDED_ARGS)" != "" ]]; then \
		if [[ "$(NO_SAVE)" != "true" ]]; then \
			echo "./build/fleet serve $(FORWARDED_ARGS)" > ~/.fleet/last-serve-invocation; \
		fi; \
		./build/fleet serve $(FORWARDED_ARGS); \
	else \
		if ! [[ -f ~/.fleet/last-serve-invocation ]]; then \
			echo "./build/fleet serve --server_address=localhost:8080 --dev --dev_license" > ~/.fleet/last-serve-invocation; \
		fi; \
		cat ~/.fleet/last-serve-invocation; \
		$$(cat ~/.fleet/last-serve-invocation); \
	fi
endif

fleet: .prefix .pre-build .pre-fleet
	CGO_ENABLED=1 go build -race=${GO_BUILD_RACE_ENABLED_VAR} -tags full,fts5,netgo -o build/${OUTPUT} -ldflags ${LDFLAGS_VERSION} ./cmd/fleet

fleet-dev: GO_BUILD_RACE_ENABLED_VAR=true
fleet-dev: fleet

fleetctl: .prefix .pre-build .pre-fleetctl
	# Race requires cgo
	$(eval CGO_ENABLED := $(shell [[ "${GO_BUILD_RACE_ENABLED_VAR}" = "true" ]] && echo 1 || echo 0))
	$(eval FLEETCTL_LDFLAGS := $(shell echo "${LDFLAGS_VERSION} ${EXTRA_FLEETCTL_LDFLAGS}"))
	CGO_ENABLED=${CGO_ENABLED} go build -race=${GO_BUILD_RACE_ENABLED_VAR} -o build/fleetctl -ldflags="${FLEETCTL_LDFLAGS}" ./cmd/fleetctl

fleetctl-dev: GO_BUILD_RACE_ENABLED_VAR=true
fleetctl-dev: fleetctl

.help-short--lint-js:
	@echo "Run the JavaScript linters"
lint-js:
	yarn lint

.help-short--lint-go:
	@echo "Run the Go linters"
lint-go:
	golangci-lint run --exclude-dirs ./node_modules --timeout 15m

.help-short--lint:
	@echo "Run linters"
.help-long--lint:
	@echo "Runs the linters for Go and Javascript code.  If linter type is not specified, all linters will be run."
.help-usage--lint:
	@echo "$(TOOL_CMD) lint [linter-type]"
.help-extra--lint:
	@echo "AVAILABLE LINTERS:"
	@echo "  go   Lint Go files with golangci-lint"
	@echo "  js   Lint .js, .jsx, .ts and .tsx files with eslint"

ifdef ARG1
lint: lint-$(ARG1)
else
lint: lint-go lint-js
endif

.help-short--test-schema:
	@echo "Update schema.sql from current migrations"
test-schema:
	go run ./tools/dbutils ./server/datastore/mysql/schema.sql ./server/mdm/android/mysql/schema.sql
dump-test-schema: test-schema

# This is the base command to run Go tests.
# Wrap this to run tests with presets (see `run-go-tests` and `test-go` targets).
# PKG_TO_TEST: Go packages to test, e.g. "server/datastore/mysql".  Separate multiple packages with spaces.
# TESTS_TO_RUN: Name specific tests to run in the specified packages.  Leave blank to run all tests in the specified packages.
# GO_TEST_EXTRA_FLAGS: Used to specify other arguments to `go test`.
# GO_TEST_MAKE_FLAGS: Internal var used by other targets to add arguments to `go test`.
PKG_TO_TEST := ""
go_test_pkg_to_test := $(addprefix ./,$(PKG_TO_TEST)) # set paths for packages to test
dlv_test_pkg_to_test := $(addprefix github.com/fleetdm/fleet/v4/,$(PKG_TO_TEST)) # set URIs for packages to debug
.run-go-tests:
ifeq ($(PKG_TO_TEST), "")
		@echo "Please specify one or more packages to test. See '$(TOOL_CMD) help run-go-tests' for more info.";
else
		@echo Running Go tests with command:
		go test -tags full,fts5,netgo -run=${TESTS_TO_RUN} ${GO_TEST_MAKE_FLAGS} ${GO_TEST_EXTRA_FLAGS} -parallel 8 -coverprofile=coverage.txt -covermode=atomic -coverpkg=github.com/fleetdm/fleet/v4/... $(go_test_pkg_to_test)
endif

# This is the base command to debug Go tests.
# Wrap this to run tests with presets (see `debug-go-tests`)
# DEBUG_TEST_EXTRA_FLAGS: Internal var used by other targets to add arguments to `dlv test`.
.debug-go-tests:
ifeq ($(PKG_TO_TEST), "")
		@echo "Please specify one or more packages to debug. See '$(TOOL_CMD) help run-go-tests' for more info.";
else
		@echo Debugging tests with command:
		dlv test ${dlv_test_pkg_to_test} --api-version=2 --listen=127.0.0.1:61179 ${DEBUG_TEST_EXTRA_FLAGS} -- -test.v -test.run=${TESTS_TO_RUN} ${GO_TEST_EXTRA_FLAGS}
endif

.help-short--run-go-tests:
	@echo "Run Go tests in specific packages"
.help-long--run-go-tests:
	@echo Command to run specific tests in development. Can run all tests for one or more packages, or specific tests within packages.
.help-options--run-go-tests:
	@echo "PKG_TO_TEST=\"pkg1 pkg2...\""
	@echo "Go packages to test, e.g. \"server/datastore/mysql\". Separate multiple packages with spaces."
	@echo "TESTS_TO_RUN=\"test\""
	@echo Name specific tests to debug in the specified packages. Leave blank to debug all tests in the specified packages.
	@echo "GO_TEST_EXTRA_FLAGS=\"--flag1 --flag2...\""
	@echo "Arguments to send to \"go test\"."
run-go-tests:
	@MYSQL_TEST=1 REDIS_TEST=1 MINIO_STORAGE_TEST=1 SAML_IDP_TEST=1 NETWORK_TEST=1 make .run-go-tests GO_TEST_MAKE_FLAGS="-v"

.help-short--debug-go-tests:
	@echo "Debug Go tests in specific packages (with Delve)"
.help-long--debug-go-tests:
	@echo Command to run specific tests in the Go debugger. Can run all tests for one or more packages, or specific tests within packages.
.help-options--debug-go-tests:
	@echo "PKG_TO_TEST=\"pkg1 pkg2...\""
	@echo "Go packages to test, e.g. \"server/datastore/mysql\". Separate multiple packages with spaces."
	@echo "TESTS_TO_RUN=\"test\""
	@echo Name specific tests to debug in the specified packages. Leave blank to debug all tests in the specified packages.
	@echo "GO_TEST_EXTRA_FLAGS=\"--flag1 --flag2...\""
	@echo "Arguments to send to \"go test\"."
debug-go-tests:
	@MYSQL_TEST=1 REDIS_TEST=1 MINIO_STORAGE_TEST=1 SAML_IDP_TEST=1 NETWORK_TEST=1 make .debug-go-tests

# Set up packages for CI testing.
DEFAULT_PKGS_TO_TEST := ./cmd/... ./ee/... ./orbit/pkg/... ./orbit/cmd/orbit ./pkg/... ./server/... ./tools/...
# fast tests are quick and do not require out-of-process dependencies (such as MySQL, etc.)
FAST_PKGS_TO_TEST := \
	./ee/server/service/hostidentity/types \
	./ee/tools/mdm \
	./orbit/pkg/cryptoinfo \
	./orbit/pkg/dataflatten \
	./orbit/pkg/keystore \
	./server/goose \
	./server/mdm/apple/appmanifest \
	./server/mdm/lifecycle \
	./server/mdm/scep/challenge \
	./server/mdm/scep/x509util \
	./server/policies
FLEETCTL_PKGS_TO_TEST := ./cmd/fleetctl/...
MYSQL_PKGS_TO_TEST := ./server/datastore/mysql/... ./server/mdm/android/mysql
SCRIPTS_PKGS_TO_TEST := ./orbit/pkg/scripts
SERVICE_PKGS_TO_TEST := ./server/service
VULN_PKGS_TO_TEST := ./server/vulnerabilities/...
ifeq ($(CI_TEST_PKG), main)
    # This is the bucket of all the tests that are not in a specific group. We take a diff between DEFAULT_PKG_TO_TEST and all the specific *_PKGS_TO_TEST.
	CI_PKG_TO_TEST=$(shell /bin/bash -c "comm -23 <(go list ${DEFAULT_PKGS_TO_TEST} | sort) <({ \
	go list $(FAST_PKGS_TO_TEST) && \
	go list $(FLEETCTL_PKGS_TO_TEST) && \
	go list $(MYSQL_PKGS_TO_TEST) && \
	go list $(SCRIPTS_PKGS_TO_TEST) && \
	go list $(SERVICE_PKGS_TO_TEST) && \
	go list $(VULN_PKGS_TO_TEST) \
	;} | sort) | sed -e 's|github.com/fleetdm/fleet/v4/||g'")
else ifeq ($(CI_TEST_PKG), fast)
	CI_PKG_TO_TEST=$(FAST_PKGS_TO_TEST)
else ifeq ($(CI_TEST_PKG), fleetctl)
	CI_PKG_TO_TEST=$(FLEETCTL_PKGS_TO_TEST)
else ifeq ($(CI_TEST_PKG), mysql)
	CI_PKG_TO_TEST=$(MYSQL_PKGS_TO_TEST)
else ifeq ($(CI_TEST_PKG), scripts)
	CI_PKG_TO_TEST=$(SCRIPTS_PKGS_TO_TEST)
else ifeq ($(CI_TEST_PKG), service)
	CI_PKG_TO_TEST=$(SERVICE_PKGS_TO_TEST)
else ifeq ($(CI_TEST_PKG), vuln)
	CI_PKG_TO_TEST=$(VULN_PKGS_TO_TEST)
else
	CI_PKG_TO_TEST=$(DEFAULT_PKGS_TO_TEST)
endif
# Command used in CI to run all tests.
.help-short--test-go:
	@echo "Run Go tests for CI"
.help-long--test-go:
	@echo "Run one or more bundle of Go tests. These are bundled together to try and make CI testing more parallelizable (and thus faster)."
.help-options--test-go:
	@echo "CI_TEST_PKG=[test package]"
	@echo "The test package bundle to run.  If not specified, all Go tests will run."
.help-extra--test-go:
	@echo "AVAILABLE TEST BUNDLES:"
	@echo "  fast"
	@echo "  service"
	@echo "  scripts"
	@echo "  mysql"
	@echo "  fleetctl"
	@echo "  vuln"
	@echo "  main        (all tests not included in other bundles)"
test-go:
	make .run-go-tests PKG_TO_TEST="$(CI_PKG_TO_TEST)"

analyze-go:
	go test -tags full,fts5,netgo -race -cover ./...

.help-short--test-js:
	@echo "Run the JavaScript tests"
test-js:
	yarn test

.help-short--test:
	@echo "Run the full test suite (lint, Go and Javascript -- used in CI)"
test: lint test-go test-js

.help-short--generate:
	@echo "Generate and bundle required Go code and Javascript code"
generate: clean-assets generate-js generate-go

generate-ci:
	NODE_OPTIONS=--openssl-legacy-provider NODE_ENV=development yarn run webpack
	make generate-go

.help-short--generate-js:
	@echo "Generate and bundle required js code"
generate-js: clean-assets .prefix
	NODE_ENV=production yarn run webpack --progress

.help-short--generate-go:
	@echo "Generate and bundle required go code"
generate-go: .prefix
	go run github.com/kevinburke/go-bindata/go-bindata -pkg=bindata -tags full \
		-o=server/bindata/generated.go \
		frontend/templates/ assets/... server/mail/templates

# we first generate the webpack bundle so that bindata knows to atch the
# output bundle file. then, generate debug bindata source file. finally, we
# run webpack in watch mode to continuously re-generate the bundle
.help-short--generate-dev:
	@echo "Generate and bundle required Javascript code in a watch loop"
generate-dev: .prefix
	NODE_ENV=development yarn run webpack --progress
	go run github.com/kevinburke/go-bindata/go-bindata -debug -pkg=bindata -tags full \
		-o=server/bindata/generated.go \
		frontend/templates/ assets/... server/mail/templates
	NODE_ENV=development yarn run webpack --progress --watch

.help-short--mock:
	@echo "Update mock data store"
mock: .prefix
	go generate github.com/fleetdm/fleet/v4/server/mock github.com/fleetdm/fleet/v4/server/mock/mockresult github.com/fleetdm/fleet/v4/server/service/mock github.com/fleetdm/fleet/v4/server/mdm/android/mock
generate-mock: mock

.help-short--doc:
	@echo "Generate updated API documentation for activities, osquery flags"
doc: .prefix
	go generate github.com/fleetdm/fleet/v4/server/fleet
	go generate github.com/fleetdm/fleet/v4/server/service/osquery_utils

generate-doc: doc vex-report

.help-short--deps:
	@echo "Install dependent programs and libraries"
deps: deps-js

deps-js:
	yarn

# check that the generated files in tools/cloner-check/generated_files match
# the current version of the cloneable structures.
check-go-cloner:
	go run ./tools/cloner-check/main.go --check

# update the files in tools/cloner-check/generated_files with the current
# version of the cloneable structures.
update-go-cloner:
	go run ./tools/cloner-check/main.go --update

.help-short--migration:
	@echo "Create a database migration file (supply name=TheNameOfYourMigration)"
migration:
	go run ./server/goose/cmd/goose -dir server/datastore/mysql/migrations/tables create $(name)
	gofmt -w server/datastore/mysql/migrations/tables/*_$(name)*.go

.help-short--clean:
	@echo "Clean all build artifacts"
clean: clean-assets
	rm -rf build vendor
	rm -f assets/bundle.js

.help-short--clean-assets:
	@echo "Clean assets only"
clean-assets:
	git clean -fx assets

fleetctl-docker: xp-fleetctl
	docker build -t fleetdm/fleetctl --platform=linux/amd64 -f tools/fleetctl-docker/Dockerfile .

bomutils-docker:
	cd tools/bomutils-docker && docker build -t fleetdm/bomutils --platform=linux/amd64 -f Dockerfile .

wix-docker:
	cd tools/wix-docker && docker build -t fleetdm/wix --platform=linux/amd64 -f Dockerfile .

.pre-binary-bundle:
	rm -rf build/binary-bundle
	mkdir -p build/binary-bundle/linux
	mkdir -p build/binary-bundle/darwin

xp-fleet: .pre-binary-bundle .pre-fleet generate
	CGO_ENABLED=1 GOOS=linux go build -tags full,fts5,netgo -trimpath -o build/binary-bundle/linux/fleet -ldflags ${LDFLAGS_VERSION} ./cmd/fleet
	CGO_ENABLED=1 GOOS=darwin go build -tags full,fts5,netgo -trimpath -o build/binary-bundle/darwin/fleet -ldflags ${LDFLAGS_VERSION} ./cmd/fleet
	CGO_ENABLED=1 GOOS=windows go build -tags full,fts5,netgo -trimpath -o build/binary-bundle/windows/fleet.exe -ldflags ${LDFLAGS_VERSION} ./cmd/fleet

xp-fleetctl: .pre-binary-bundle .pre-fleetctl generate-go
	CGO_ENABLED=0 GOOS=linux go build -trimpath -o build/binary-bundle/linux/fleetctl -ldflags ${LDFLAGS_VERSION} ./cmd/fleetctl
	CGO_ENABLED=0 GOOS=darwin go build -trimpath -o build/binary-bundle/darwin/fleetctl -ldflags ${LDFLAGS_VERSION} ./cmd/fleetctl
	CGO_ENABLED=0 GOOS=windows go build -trimpath -o build/binary-bundle/windows/fleetctl.exe -ldflags ${LDFLAGS_VERSION} ./cmd/fleetctl

binary-bundle: xp-fleet xp-fleetctl
	cd build/binary-bundle && zip -r fleet.zip darwin/ linux/ windows/
	cd build/binary-bundle && mkdir fleetctl-macos && cp darwin/fleetctl fleetctl-macos && tar -czf fleetctl-macos.tar.gz fleetctl-macos
	cd build/binary-bundle && mkdir fleetctl-linux && cp linux/fleetctl fleetctl-linux && tar -czf fleetctl-linux.tar.gz fleetctl-linux
	cd build/binary-bundle && mkdir fleetctl-windows && cp windows/fleetctl.exe fleetctl-windows && tar -czf fleetctl-windows.tar.gz fleetctl-windows
	cd build/binary-bundle && cp windows/fleetctl.exe . && zip fleetctl.exe.zip fleetctl.exe
	cd build/binary-bundle && shasum -a 256 fleet.zip fleetctl.exe.zip fleetctl-macos.tar.gz fleetctl-windows.tar.gz fleetctl-linux.tar.gz

# Build orbit/fleetd fleetd_tables extension
fleetd-tables-windows:
	GOOS=windows GOARCH=amd64 go build -o fleetd_tables_windows.exe ./orbit/cmd/fleetd_tables
fleetd-tables-windows-arm64:
	GOOS=windows GOARCH=arm64 go build -o fleetd_tables_windows_arm64.exe ./orbit/cmd/fleetd_tables
fleetd-tables-linux:
	GOOS=linux GOARCH=amd64 go build -o fleetd_tables_linux.ext ./orbit/cmd/fleetd_tables
fleetd-tables-linux-arm64:
	GOOS=linux GOARCH=arm64 go build -o fleetd_tables_linux_arm64.ext ./orbit/cmd/fleetd_tables
fleetd-tables-darwin:
	GOOS=darwin GOARCH=amd64 go build -o fleetd_tables_darwin.ext ./orbit/cmd/fleetd_tables
fleetd-tables-darwin_arm64:
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go build -o fleetd_tables_darwin_arm64.ext ./orbit/cmd/fleetd_tables
fleetd-tables-darwin-universal: fleetd-tables-darwin fleetd-tables-darwin_arm64
	lipo -create fleetd_tables_darwin.ext fleetd_tables_darwin_arm64.ext -output fleetd_tables_darwin_universal.ext
fleetd-tables-all: fleetd-tables-windows fleetd-tables-linux fleetd-tables-darwin-universal fleetd-tables-linux-arm64 fleetd-tables-windows-arm64
fleetd-tables-clean:
	rm -f fleetd_tables_windows.exe fleetd_tables_linux.ext fleetd_tables_linux_arm64.ext fleetd_tables_darwin.ext fleetd_tables_darwin_arm64.ext fleetd_tables_darwin_universal.ext

.pre-binary-arch:
ifndef GOOS
	@echo "GOOS is Empty. Try use to see valid GOOS/GOARCH platform: go tool dist list. Ex.: make binary-arch GOOS=linux GOARCH=arm64"
	@exit 1;
endif
ifndef GOARCH
	@echo "GOARCH is Empty. Try use to see valid GOOS/GOARCH platform: go tool dist list. Ex.: make binary-arch GOOS=linux GOARCH=arm64"
	@exit 1;
endif


binary-arch: .pre-binary-arch .pre-binary-bundle .pre-fleet
	mkdir -p build/binary-bundle/${GOARCH}-${GOOS}
	CGO_ENABLED=1 GOARCH=${GOARCH} GOOS=${GOOS} go build -tags full,fts5,netgo -o build/binary-bundle/${GOARCH}-${GOOS}/fleet -ldflags ${LDFLAGS_VERSION} ./cmd/fleet
	CGO_ENABLED=0 GOARCH=${GOARCH} GOOS=${GOOS} go build -tags full,fts5,netgo -o build/binary-bundle/${GOARCH}-${GOOS}/fleetctl -ldflags ${LDFLAGS_VERSION} ./cmd/fleetctl
	cd build/binary-bundle/${GOARCH}-${GOOS} && tar -czf fleetctl-${GOARCH}-${GOOS}.tar.gz fleetctl fleet


# Drop, create, and migrate the e2e test database
e2e-reset-db:
	docker compose exec -T mysql_test bash -c 'echo "drop database if exists e2e; create database e2e;" | MYSQL_PWD=toor mysql -uroot'
	./build/fleet prepare db --mysql_address=localhost:3307  --mysql_username=root --mysql_password=toor --mysql_database=e2e

e2e-setup:
	./build/fleetctl config set --context e2e --address https://localhost:8642 --tls-skip-verify true
	./build/fleetctl setup --context e2e --email=admin@example.com --password=password123# --org-name='Fleet Test' --name Admin
	./build/fleetctl user create --context e2e --email=maintainer@example.com --name maintainer --password=password123# --global-role=maintainer
	./build/fleetctl user create --context e2e --email=observer@example.com --name observer --password=password123# --global-role=observer
	./build/fleetctl user create --context e2e --email=sso_user@example.com --name "SSO user" --sso=true

# Setup e2e test environment and pre-populate database with software and vulnerabilities fixtures.
#
# Use in lieu of `e2e-setup` for tests that depend on these fixtures
e2e-setup-with-software:
	curl 'https://localhost:8642/api/v1/setup' \
		--data-raw '{"server_url":"https://localhost:8642","org_info":{"org_name":"Fleet Test"},"admin":{"admin":true,"email":"admin@example.com","name":"Admin","password":"password123#","password_confirmation":"password123#"}}' \
		--compressed \
		--insecure
	./tools/backup_db/restore_e2e_software_test.sh

e2e-serve-free: e2e-reset-db
	./build/fleet serve --mysql_address=localhost:3307 --mysql_username=root --mysql_password=toor --mysql_database=e2e --server_address=0.0.0.0:8642

e2e-serve-premium: e2e-reset-db
	./build/fleet serve  --dev_license --mysql_address=localhost:3307 --mysql_username=root --mysql_password=toor --mysql_database=e2e --server_address=0.0.0.0:8642

# Associate a host with a Fleet Desktop token.
#
# Usage:
# make e2e-set-desktop-token host_id=1 token=foo
e2e-set-desktop-token:
	docker compose exec -T mysql_test bash -c 'echo "INSERT INTO e2e.host_device_auth (host_id, token) VALUES ($(host_id), \"$(token)\") ON DUPLICATE KEY UPDATE token=VALUES(token)" | MYSQL_PWD=toor mysql -uroot'

changelog:
	find changes -type f ! -name .keep -exec awk 'NF' {} + > new-CHANGELOG.md
	sh -c "cat new-CHANGELOG.md CHANGELOG.md > tmp-CHANGELOG.md && rm new-CHANGELOG.md && mv tmp-CHANGELOG.md CHANGELOG.md"
	sh -c "git rm changes/*"

changelog-orbit:
	$(eval TODAY_DATE := $(shell date "+%b %d, %Y"))
	@echo -e "## Orbit $(version) ($(TODAY_DATE))\n" > new-CHANGELOG.md
	sh -c "find orbit/changes -type file | grep -v .keep | xargs -I {} sh -c 'grep \"\S\" {} | sed -E "s/^-/*/"; echo' >> new-CHANGELOG.md"
	sh -c "cat new-CHANGELOG.md orbit/CHANGELOG.md > tmp-CHANGELOG.md && rm new-CHANGELOG.md && mv tmp-CHANGELOG.md orbit/CHANGELOG.md"
	sh -c "git rm orbit/changes/*"

changelog-chrome:
	$(eval TODAY_DATE := $(shell date "+%b %d, %Y"))
	@echo -e "## fleetd-chrome $(version) ($(TODAY_DATE))\n" > new-CHANGELOG.md
	sh -c "find ee/fleetd-chrome/changes -type file | grep -v .keep | xargs -I {} sh -c 'grep \"\S\" {}; echo' >> new-CHANGELOG.md"
	sh -c "cat new-CHANGELOG.md ee/fleetd-chrome/CHANGELOG.md > tmp-CHANGELOG.md && rm new-CHANGELOG.md && mv tmp-CHANGELOG.md ee/fleetd-chrome/CHANGELOG.md"
	sh -c "git rm ee/fleetd-chrome/changes/*"

# Updates the documentation for the currently released versions of fleetd components in old Fleet's TUF (tuf.fleetctl.com).
fleetd-old-tuf:
	sh -c 'echo "<!-- DO NOT EDIT. This document is automatically generated by running \`make fleetd-old-tuf\`. -->\n# tuf.fleetctl.com\n\nFollowing are the currently deployed versions of fleetd components on the \`stable\` and \`edge\` channel.\n" > orbit/old-TUF.md'
	sh -c 'echo "## \`stable\`\n" >> orbit/old-TUF.md'
	sh -c 'go run tools/tuf/status/tuf-status.go channel-version -url https://tuf.fleetctl.com -channel stable -format markdown >> orbit/old-TUF.md'
	sh -c 'echo "\n## \`edge\`\n" >> orbit/old-TUF.md'
	sh -c 'go run tools/tuf/status/tuf-status.go channel-version -url https://tuf.fleetctl.com -channel edge -format markdown >> orbit/old-TUF.md'

# Updates the documentation for the currently released versions of fleetd components in Fleet's TUF (updates.fleetdm.com).
fleetd-tuf:
	sh -c 'echo "<!-- DO NOT EDIT. This document is automatically generated by running \`make fleetd-tuf\`. -->\n# updates.fleetdm.com\n\nFollowing are the currently deployed versions of fleetd components on the \`stable\` and \`edge\` channel.\n" > orbit/TUF.md'
	sh -c 'echo "## \`stable\`\n" >> orbit/TUF.md'
	sh -c 'go run tools/tuf/status/tuf-status.go channel-version -channel stable -format markdown >> orbit/TUF.md'
	sh -c 'echo "\n## \`edge\`\n" >> orbit/TUF.md'
	sh -c 'go run tools/tuf/status/tuf-status.go channel-version -channel edge -format markdown >> orbit/TUF.md'

###
# Development DB commands
###

# Reset the development DB
db-reset:
	docker compose exec -T mysql bash -c 'echo "drop database if exists fleet; create database fleet;" | MYSQL_PWD=toor mysql -uroot'
	./build/fleet prepare db --dev

# Back up the development DB to file
db-backup:
	./tools/backup_db/backup.sh

# Restore the development DB from file
db-restore:
	./tools/backup_db/restore.sh


# Interactive snapshot / restore
.help-short--snap .help-short--snapshot:
	@echo "Snapshot the database"
.help-long--snap .help-long--snapshot:
	@echo "Interactively take a snapshot of the present database state. Restore snapshots with \`$(TOOL_CMD) restore\`."

SNAPSHOT_BINARY = ./build/snapshot
snap snapshot: $(SNAPSHOT_BINARY)
	@ $(SNAPSHOT_BINARY) snapshot
$(SNAPSHOT_BINARY): tools/snapshot/*.go
	cd tools/snapshot && go build -o ../../build/snapshot

.help-short--restore:
	@echo "Restore a database snapshot"
.help-long--restore:
	@echo "Interactively restore database state using a snapshot taken with \`$(TOOL_CMD) snapshot\`."
.help-options--restore:
	@echo "PREPARE (alias: PREP)"
	@echo "Run migrations after restoring the snapshot"

restore: $(SNAPSHOT_BINARY)
	@$(SNAPSHOT_BINARY) restore
	@if [[ "$(PREP)" == "true" || "$(PREPARE)" == "true" ]]; then \
		echo "Running migrations..."; \
		./build/fleet prepare db --dev; \
	fi
	@echo Done!

# Generate osqueryd.app.tar.gz bundle from osquery.io.
#
# Usage:
# make osqueryd-app-tar-gz version=5.1.0 out-path=.
osqueryd-app-tar-gz:
ifneq ($(shell uname), Darwin)
	@echo "Makefile target osqueryd-app-tar-gz is only supported on macOS"
	@exit 1
endif
	$(eval TMP_DIR := $(shell mktemp -d))
	curl -L https://github.com/osquery/osquery/releases/download/$(version)/osquery-$(version).pkg --output $(TMP_DIR)/osquery-$(version).pkg
	pkgutil --expand $(TMP_DIR)/osquery-$(version).pkg $(TMP_DIR)/osquery_pkg_expanded
	rm -rf $(TMP_DIR)/osquery_pkg_payload_expanded
	mkdir -p $(TMP_DIR)/osquery_pkg_payload_expanded
	tar xf $(TMP_DIR)/osquery_pkg_expanded/Payload --directory $(TMP_DIR)/osquery_pkg_payload_expanded
	$(TMP_DIR)/osquery_pkg_payload_expanded/opt/osquery/lib/osquery.app/Contents/MacOS/osqueryd --version
	tar czf $(out-path)/osqueryd.app.tar.gz -C $(TMP_DIR)/osquery_pkg_payload_expanded/opt/osquery/lib osquery.app
	rm -r $(TMP_DIR)

# Generate nudge.app.tar.gz bundle from nudge repo.
#
# Usage:
# make nudge-app-tar-gz version=1.1.10.81462 out-path=.
nudge-app-tar-gz:
ifneq ($(shell uname), Darwin)
	@echo "Makefile target nudge-app-tar-gz is only supported on macOS"
	@exit 1
endif
	$(eval TMP_DIR := $(shell mktemp -d))
	curl -L https://github.com/macadmins/nudge/releases/download/v$(version)/Nudge-$(version).pkg --output $(TMP_DIR)/nudge-$(version).pkg
	pkgutil --expand $(TMP_DIR)/nudge-$(version).pkg $(TMP_DIR)/nudge_pkg_expanded
	mkdir -p $(TMP_DIR)/nudge_pkg_payload_expanded
	tar xvf $(TMP_DIR)/nudge_pkg_expanded/nudge-$(version).pkg/Payload --directory $(TMP_DIR)/nudge_pkg_payload_expanded
	$(TMP_DIR)/nudge_pkg_payload_expanded/Nudge.app/Contents/MacOS/Nudge --version
	tar czf $(out-path)/nudge.app.tar.gz -C $(TMP_DIR)/nudge_pkg_payload_expanded/ Nudge.app
	rm -r $(TMP_DIR)

# Generate swiftDialog.app.tar.gz bundle from the swiftDialog repo.
#
# Usage:
# make swift-dialog-app-tar-gz version=2.2.1 build=4591 out-path=.
swift-dialog-app-tar-gz:
ifneq ($(shell uname), Darwin)
	@echo "Makefile target swift-dialog-app-tar-gz is only supported on macOS"
	@exit 1
endif
	# locking the version of swiftDialog to 2.2.1-4591 as newer versions
	# might have layout issues.
ifneq ($(version), 2.2.1)
	@echo "Version is locked at 2.1.0, see comments in Makefile target for details"
	@exit 1
endif

ifneq ($(build), 4591)
	@echo "Build version is locked at 4591, see comments in Makefile target for details"
	@exit 1
endif
	$(eval TMP_DIR := $(shell mktemp -d))
	curl -L https://github.com/swiftDialog/swiftDialog/releases/download/v$(version)/dialog-$(version)-$(build).pkg --output $(TMP_DIR)/swiftDialog-$(version).pkg
	pkgutil --expand $(TMP_DIR)/swiftDialog-$(version).pkg $(TMP_DIR)/swiftDialog_pkg_expanded
	mkdir -p $(TMP_DIR)/swiftDialog_pkg_payload_expanded
	tar xvf $(TMP_DIR)/swiftDialog_pkg_expanded/tmp-package.pkg/Payload --directory $(TMP_DIR)/swiftDialog_pkg_payload_expanded
	$(TMP_DIR)/swiftDialog_pkg_payload_expanded/Library/Application\ Support/Dialog/Dialog.app/Contents/MacOS/Dialog --version
	tar czf $(out-path)/swiftDialog.app.tar.gz -C $(TMP_DIR)/swiftDialog_pkg_payload_expanded/Library/Application\ Support/Dialog/ Dialog.app
	rm -rf $(TMP_DIR)

# Generate escrowBuddy.pkg bundle from the Escrow Buddy repo.
#
# Usage:
# make escrow-buddy-pkg version=1.0.0 out-path=.
escrow-buddy-pkg:
	curl -L https://github.com/macadmins/escrow-buddy/releases/download/v$(version)/Escrow.Buddy-$(version).pkg --output $(out-path)/escrowBuddy.pkg


# Build and generate desktop.app.tar.gz bundle.
#
# Usage:
# FLEET_DESKTOP_APPLE_AUTHORITY=foo FLEET_DESKTOP_VERSION=0.0.1 make desktop-app-tar-gz
#
# Output: desktop.app.tar.gz
desktop-app-tar-gz:
ifneq ($(shell uname), Darwin)
	@echo "Makefile target desktop-app-tar-gz is only supported on macOS"
	@exit 1
endif
	go run ./tools/desktop macos

FLEET_DESKTOP_VERSION ?= unknown

# Build desktop executable for Windows.
# This generates desktop executable for Windows that includes versioninfo binary properties
# These properties can be displayed when right-click on the binary in Windows Explorer.
# See: https://docs.microsoft.com/en-us/windows/win32/menurc/versioninfo-resource
# To sign this binary with a certificate, use signtool.exe or osslsigncode tool
#
# Usage:
# FLEET_DESKTOP_VERSION=0.0.1 make desktop-windows
#
# Output: fleet-desktop.exe
desktop-windows:
	go run ./orbit/tools/build/build-windows.go -version $(FLEET_DESKTOP_VERSION) -input ./orbit/cmd/desktop -output fleet-desktop.exe

# Build desktop executable for Windows.
# This generates desktop executable for Windows that includes versioninfo binary properties
# These properties can be displayed when right-click on the binary in Windows Explorer.
# See: https://docs.microsoft.com/en-us/windows/win32/menurc/versioninfo-resource
# To sign this binary with a certificate, use signtool.exe or osslsigncode tool
#
# Usage:
# FLEET_DESKTOP_VERSION=0.0.1 make desktop-windows-arm64
#
# Output: fleet-desktop.exe
desktop-windows-arm64:
	go run ./orbit/tools/build/build-windows.go -version $(FLEET_DESKTOP_VERSION) -input ./orbit/cmd/desktop -output fleet-desktop.exe -arch arm64

# Build desktop executable for Linux.
#
# Usage:
# FLEET_DESKTOP_VERSION=0.0.1 make desktop-linux
#
# Output: desktop.tar.gz
desktop-linux:
	docker build -f Dockerfile-desktop-linux -t desktop-linux-builder .
	docker run --rm -v $(shell pwd):/output desktop-linux-builder /bin/bash -c "\
		mkdir -p /output/fleet-desktop && \
		go build -o /output/fleet-desktop/fleet-desktop -ldflags "-X=main.version=$(FLEET_DESKTOP_VERSION)" /usr/src/fleet/orbit/cmd/desktop && \
		cd /output && \
		tar czf desktop.tar.gz fleet-desktop && \
		rm -r fleet-desktop"

# Build desktop executable for Linux ARM.
#
# Usage:
# FLEET_DESKTOP_VERSION=0.0.1 make desktop-linux-arm64
#
# Output: desktop.tar.gz
desktop-linux-arm64:
	docker build -f Dockerfile-desktop-linux -t desktop-linux-builder .
	docker run --rm -v $(shell pwd):/output desktop-linux-builder /bin/bash -c "\
		mkdir -p /output/fleet-desktop && \
		GOARCH=arm64 go build -o /output/fleet-desktop/fleet-desktop -ldflags "-X=main.version=$(FLEET_DESKTOP_VERSION)" /usr/src/fleet/orbit/cmd/desktop && \
		cd /output && \
		tar czf desktop.tar.gz fleet-desktop && \
		rm -r fleet-desktop"

# Build orbit executable for Windows.
# This generates orbit executable for Windows that includes versioninfo binary properties
# These properties can be displayed when right-click on the binary in Windows Explorer.
# See: https://docs.microsoft.com/en-us/windows/win32/menurc/versioninfo-resource
# To sign this binary with a certificate, use signtool.exe or osslsigncode tool
#
# Usage:
# ORBIT_VERSION=0.0.1 make orbit-windows
#
# Output: orbit.exe
orbit-windows:
	go run ./orbit/tools/build/build-windows.go -version $(ORBIT_VERSION) -input ./orbit/cmd/orbit -output orbit.exe

# Build orbit executable for Windows.
# This generates orbit executable for Windows that includes versioninfo binary properties
# These properties can be displayed when right-click on the binary in Windows Explorer.
# See: https://docs.microsoft.com/en-us/windows/win32/menurc/versioninfo-resource
# To sign this binary with a certificate, use signtool.exe or osslsigncode tool
#
# Usage:
# ORBIT_VERSION=0.0.1 make orbit-windows-arm64
#
# Output: orbit.exe
orbit-windows-arm64:
	go run ./orbit/tools/build/build-windows.go -version $(ORBIT_VERSION) -input ./orbit/cmd/orbit -output orbit.exe -arch arm64

# db-replica-setup setups one main and one read replica MySQL instance for dev/testing.
#	- Assumes the docker containers are already running (tools/mysql-replica-testing/docker-compose.yml)
# 	- MySQL instance listening on 3308 is the main instance.
# 	- MySQL instance listening on 3309 is the read instance.
#	- Sets a delay of 1s for replication.
db-replica-setup:
	$(eval MYSQL_REPLICATION_USER := replicator)
	$(eval MYSQL_REPLICATION_PASSWORD := rotacilper)
	MYSQL_PWD=toor mysql --host 127.0.0.1 --port 3309 -uroot -AN -e "stop slave; reset slave all;"
	MYSQL_PWD=toor mysql --host 127.0.0.1 --port 3308 -uroot -AN -e "drop user if exists '$(MYSQL_REPLICATION_USER)'; create user '$(MYSQL_REPLICATION_USER)'@'%' identified by '$(MYSQL_REPLICATION_PASSWORD)'; grant replication slave on *.* to '$(MYSQL_REPLICATION_USER)'@'%'; flush privileges;"
	$(eval MAIN_POSITION := $(shell MYSQL_PWD=toor mysql --host 127.0.0.1 --port 3308 -uroot -e 'show master status \G' | grep Position | grep -o '[0-9]*'))
	$(eval MAIN_FILE := $(shell MYSQL_PWD=toor mysql --host 127.0.0.1 --port 3308 -uroot -e 'show master status \G' | grep File | sed -n -e 's/^.*: //p'))
	MYSQL_PWD=toor mysql --host 127.0.0.1 --port 3309 -uroot -AN -e "change master to master_port=3306,master_host='mysql_main',master_user='$(MYSQL_REPLICATION_USER)',master_password='$(MYSQL_REPLICATION_PASSWORD)',master_log_file='$(MAIN_FILE)',master_log_pos=$(MAIN_POSITION);"
	if [ "${FLEET_MYSQL_IMAGE}" == "mysql:8.0" ]; then MYSQL_PWD=toor mysql --host 127.0.0.1 --port 3309 -uroot -AN -e "change master to get_master_public_key=1;"; fi
	MYSQL_PWD=toor mysql --host 127.0.0.1 --port 3309 -uroot -AN -e "change master to master_delay=1;"
	MYSQL_PWD=toor mysql --host 127.0.0.1 --port 3309 -uroot -AN -e "start slave;"

# db-replica-reset resets the main MySQL instance.
db-replica-reset: fleet
	MYSQL_PWD=toor mysql --host 127.0.0.1 --port 3308 -uroot -e "drop database if exists fleet; create database fleet;"
	FLEET_MYSQL_ADDRESS=127.0.0.1:3308 ./build/fleet prepare db --dev

# db-replica-run runs fleet serve with one main and one read MySQL instance.
db-replica-run: fleet
	FLEET_MYSQL_ADDRESS=127.0.0.1:3308 FLEET_MYSQL_READ_REPLICA_ADDRESS=127.0.0.1:3309 FLEET_MYSQL_READ_REPLICA_USERNAME=fleet FLEET_MYSQL_READ_REPLICA_DATABASE=fleet FLEET_MYSQL_READ_REPLICA_PASSWORD=insecure ./build/fleet serve --dev --dev_license

vex-report:
	sh -c 'echo "<!-- DO NOT EDIT. This document is automatically generated by running \`make vex-report\`. -->\n# Vulnerability Report\n\nFollowing is the vulnerability report of Fleet and its dependencies.\n" > security/status.md'
	sh -c 'echo "## \`fleetdm/fleet\` docker image\n" >> security/status.md'
	sh -c 'go run ./tools/vex-parser ./security/vex/fleet >> security/status.md'
	sh -c 'echo "## \`fleetdm/fleetctl\` docker image\n" >> security/status.md'
	sh -c 'go run ./tools/vex-parser ./security/vex/fleetctl >> security/status.md'

# make update-go version=1.24.4
UPDATE_GO_DOCKERFILES := ./Dockerfile-desktop-linux ./infrastructure/loadtesting/terraform/docker/loadtest.Dockerfile ./tools/mdm/migration/mdmproxy/Dockerfile
UPDATE_GO_MODS := go.mod ./tools/mdm/windows/bitlocker/go.mod ./tools/snapshot/go.mod ./tools/terraform/go.mod
update-go:
	@test $(version) || (echo "Mising 'version' argument, usage: 'make update-go version=1.24.4'" ; exit 1)
	@for dockerfile in $(UPDATE_GO_DOCKERFILES) ; do \
		go run ./tools/tuf/replace $$dockerfile "golang:.+-" "golang:$(version)-" ; \
		echo "Please update sha256 in $$dockerfile" ; \
	done
	@for gomod in $(UPDATE_GO_MODS) ; do \
		go run ./tools/tuf/replace $$gomod "(?m)^go .+$$" "go $(version)" ; \
	done
	@echo "* Updated go to $(version)" > changes/update-go-$(version)
	@cp changes/update-go-$(version) orbit/changes/update-go-$(version)

include ./tools/makefile-support/helpsystem-targets
