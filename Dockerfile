FROM alpine:latest
RUN apk --no-cache add ca-certificates
COPY lfscache /bin/lfscache
ENTRYPOINT ["/bin/lfscache"]
