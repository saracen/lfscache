FROM golang:1.18 as builder

ARG VERSION=latest

WORKDIR /go
RUN GOOS=linux GOARCH=386 GOPATH=/go go install -v github.com/tgorol/lfscache@${VERSION}

FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root
COPY --from=builder /go/bin/linux_386/lfscache ./

CMD ["--help"]
ENTRYPOINT ["/root/lfscache"]
