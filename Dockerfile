FROM golang:1.18 as builder

WORKDIR /go
RUN GOOS=linux GOARCH=386 GOPATH=/go go install -v github.com/tgorol/lfscache@v0.1.5


FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root
COPY --from=builder /go/bin/linux_386/lfscache ./

CMD ["--help"]
ENTRYPOINT ["/root/lfscache"]
