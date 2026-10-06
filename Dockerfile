# Builds a static gosearch binary and runs it from a scratch container.
# The server only does HTTP, so no CA bundle or glibc is needed.
FROM golang:1.25 AS build
WORKDIR /src

COPY go.mod ./
COPY cmd/ ./cmd/
COPY pkg/ ./pkg/

RUN CGO_ENABLED=0 go build -trimpath -o /out/gosearch ./cmd/gosearch

FROM scratch
COPY --from=build /out/gosearch /gosearch
EXPOSE 9200
ENTRYPOINT ["/gosearch"]
CMD ["-addr", ":9200"]