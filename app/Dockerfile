FROM node:22-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM node:22-alpine AS build
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
# Placeholder only so the build doesn't complain; the real secret comes from .env at runtime.
ENV NEXT_TELEMETRY_DISABLED=1 SKIP_MIGRATIONS=1 BETTER_AUTH_SECRET=build-placeholder-not-used-at-runtime
RUN npx next build

FROM node:22-alpine
WORKDIR /app
# pg_dump / pg_restore for backups of DATBase's own database (must match Postgres 16)
RUN apk add --no-cache postgresql16-client
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 PORT=3000 HOSTNAME=0.0.0.0
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static ./.next/static
COPY --from=build /app/drizzle ./drizzle
COPY --from=build /app/scripts ./scripts
# The backup tool needs the full S3 SDK (the standalone bundle only carries what the app traced).
COPY --from=deps /app/node_modules/@aws-sdk ./node_modules/@aws-sdk
COPY --from=deps /app/node_modules/@smithy ./node_modules/@smithy
COPY --from=deps /app/node_modules/@aws ./node_modules/@aws
EXPOSE 3000
# Runs as root because it needs the Docker socket to create project containers.
CMD ["node", "server.js"]
