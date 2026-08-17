.PHONY: fmt test vet check

fmt:
	gofmt -w .

test:
	go test ./...

vet:
	go vet ./...

check: test vet
