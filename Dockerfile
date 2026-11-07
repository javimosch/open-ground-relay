FROM golang:1.22-alpine AS build
WORKDIR /src
COPY . .
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /og-relay .

FROM scratch
COPY --from=build /og-relay /og-relay
USER 47160:47160
EXPOSE 47160
ENTRYPOINT ["/og-relay", "-addr", ":47160"]
