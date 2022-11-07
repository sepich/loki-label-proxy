VER ?= `git show -s --format=%cd-%h --date=format:%y%m%d`

help: ## Displays help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-z0-9A-Z_-]+:.*?##/ { printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build binaries with version set
	@CGO_ENABLED=0 go build -ldflags "-w -s \
	-X github.com/prometheus/common/version.Version=${VER} \
	-X github.com/prometheus/common/version.Revision=`git rev-parse --short HEAD` \
	-X github.com/prometheus/common/version.Branch=`git rev-parse --abbrev-ref HEAD` \
	-X github.com/prometheus/common/version.BuildUser=${USER}@`hostname` \
	-X github.com/prometheus/common/version.BuildDate=`date +%Y/%m/%d-%H:%M:%SZ`"

docker: ## Build docker image
	@docker build -t loki-label-proxy .