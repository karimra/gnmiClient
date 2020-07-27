FROM golang:1.13 as builder
ADD . /build
WORKDIR /build
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o gnmic .

FROM alpine
COPY --from=builder /build/gnmic /app/
WORKDIR /app
ENTRYPOINT [ "/app/gnmic" ]
CMD [ "help" ]
