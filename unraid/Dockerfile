FROM golang:1.24-alpine AS build

WORKDIR /src

RUN apk add --no-cache gcc g++ git bash libpcap-dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o /out/phev2mqtt .

FROM alpine:3.24

RUN apk add --no-cache bash libpcap-dev

COPY --from=build /out/phev2mqtt /usr/local/bin/phev2mqtt
COPY unraid/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENV mqtt_server=192.168.1.1:1883 \
    mqtt_user=user \
    mqtt_password=password \
    phev_register=false \
    debug=true \
    extraarg=true

ENTRYPOINT ["bash", "/entrypoint.sh"]
