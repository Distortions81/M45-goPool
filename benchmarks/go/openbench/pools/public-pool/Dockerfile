FROM node:24.16.0-bookworm-slim AS build

RUN apt-get update && apt-get upgrade -y
RUN DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        python3 build-essential cmake curl ca-certificates \
    && apt clean && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*

WORKDIR /build
COPY . .
RUN npm i && npm run build

FROM node:24.16.0-bookworm-slim

EXPOSE 3333 3334 8332
WORKDIR /public-pool
COPY --from=build /build .

RUN mkdir -p /public-pool/DB
CMD ["/usr/local/bin/node", "dist/main"]
