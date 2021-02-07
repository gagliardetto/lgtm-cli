.DEFAULT_GOAL := install
install:
	go build -ldflags "-X main.gitCommitSHA=$$(git rev-list -1 HEAD)" -o $$GOPATH/bin/lgtm
