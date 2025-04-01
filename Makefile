.PHONY: all gocast test

all:
	$(MAKE) gocast

gocast:
	go build .

debug:
	go build -race .

test:
	go test -v -race -short -failfast ./...

linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o gocast .
