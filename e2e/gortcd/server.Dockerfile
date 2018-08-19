FROM golang:latest

RUN go get github.com/gortc/gortcd
COPY gortcd.yml .

CMD ["gortcd"]
