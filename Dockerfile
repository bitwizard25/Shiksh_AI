# syntax=docker/dockerfile:1
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/shiksha ./cmd/shiksha

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/shiksha /shiksha
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/shiksha"]
CMD ["serve", "--migrate=false"]
