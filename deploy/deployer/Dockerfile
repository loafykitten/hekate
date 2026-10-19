FROM alpine:3.22
RUN apk add --no-cache bash curl jq docker-cli docker-cli-compose
COPY deployer.sh /usr/local/bin/deployer.sh
# Not COPY --chmod: hosts without BuildKit don't support it.
RUN chmod 0755 /usr/local/bin/deployer.sh
ENTRYPOINT ["/usr/local/bin/deployer.sh"]
