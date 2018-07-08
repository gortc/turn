ARG CI_GO_VERSION
FROM golang:${CI_GO_VERSION}

ADD . /go/src/github.com/gortc/turn

WORKDIR /go/src/github.com/gortc/turn/integration-test

RUN go install .

CMD ["integration-test"]

