FROM alpine:latest
COPY lfscache /bin/lfscache
ENTRYPOINT ["/bin/lfscache"]
