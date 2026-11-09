FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /pay ./cmd/server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 pay && mkdir /data && chown pay /data
COPY --from=build /pay /usr/local/bin/pay
USER pay
ENV PAY_MODE=live HTTP_ADDR=0.0.0.0:8080 DATABASE_PATH=/data/pay.db
EXPOSE 8080
VOLUME /data
ENTRYPOINT ["pay"]
