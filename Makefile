.PHONY: fmt
# fmt code
fmt:
	gofmt -s -w -r 'interface{} -> any' ./ && \
	goimports -w ./