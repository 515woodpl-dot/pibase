FROM alpine:3.20

# 0.39.x: the last line before PocketBase moved to Go 1.27's new JSON engine (0.40.0), whose author
# advises against blind production upgrades. Two runtime crashes were seen on 0.40.4 (review AI-01).
ARG PB_VERSION=0.39.11
# Set automatically by `docker build`/compose to the machine's architecture (amd64, arm64).
# Downloading the matching binary avoids running PocketBase under CPU emulation.
ARG TARGETARCH

RUN apk add --no-cache ca-certificates unzip wget \
 && ARCH="${TARGETARCH:-$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')}" \
 && wget -q "https://github.com/pocketbase/pocketbase/releases/download/v${PB_VERSION}/pocketbase_${PB_VERSION}_linux_${ARCH}.zip" -O /tmp/pb.zip \
 && unzip /tmp/pb.zip -d /pb \
 && rm /tmp/pb.zip \
 && /pb/pocketbase --version

# DATBase hook: usage reporting + plan-limit enforcement (read-only, customers can't change it)
COPY pb_hooks /pb/pb_hooks

EXPOSE 8090

# --automigrate=false: tables live in the data volume; don't write migration files into the container.
CMD ["/pb/pocketbase", "serve", "--http=0.0.0.0:8090", "--dir=/pb/pb_data", "--hooksDir=/pb/pb_hooks", "--automigrate=false"]
