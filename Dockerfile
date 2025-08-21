FROM golang:1.24.2 AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /konsole

FROM debian:bookwormwa

RUN apt-get update && apt-get install -y \
    traceroute \
    curl \
    iputils-ping \
    iproute2 \
    net-tools \
    dnsutils \
    whois \
    wget \
    lsof \
    neofetch \
    htop \
    util-linux \
    && rm -rf /var/lib/apt/lists/* \
    && addgroup --system user \
    && adduser --system --ingroup user --disabled-password --shell /bin/bash user

COPY --from=builder /konsole /konsole

CMD ["/konsole"]
