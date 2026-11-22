# Base images are pinned by digest so a build cannot silently change; refresh the digests on purpose
# (docker pull <tag>, then docker inspect --format '{{index .RepoDigests 0}}' <tag>).
FROM node:22-alpine@sha256:1314fb30ec37592d5845e5a160d522c0e680972aa534549940b8d73bde88481a AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/index.html web/landing.html web/privacy.html web/vite.config.js web/qr.js ./
COPY web/src ./src
RUN mkdir -p dist && npm run build

FROM golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bestregards ./cmd/bestregards

FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
COPY --from=build /bestregards /bestregards
EXPOSE 8080
ENTRYPOINT ["/bestregards"]
