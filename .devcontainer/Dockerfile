FROM golang:1.20.5-buster

RUN apt-get update -qq && \
    apt-get install -y sudo build-essential bash && \
    apt-get clean

# install go tools
RUN go install golang.org/x/tools/gopls@latest  && \ 
    go install github.com/acroca/go-symbols@latest  && \ 
    go install github.com/cweill/gotests/gotests@latest  && \ 
    go install github.com/davidrjenni/reftools/cmd/fillstruct@latest  && \ 
    go install github.com/haya14busa/goplay/cmd/goplay@latest  && \ 
    go install github.com/stamblerre/gocode@latest  && \ 
    go install github.com/mdempsky/gocode@latest  && \ 
    go install github.com/ramya-rao-a/go-outline@latest  && \ 
    go install github.com/rogpeppe/godef@latest  && \ 
    go install github.com/sqs/goreturns@latest  && \ 
    go install github.com/uudashr/gopkgs/v2/cmd/gopkgs@latest  && \ 
    go install github.com/zmb3/gogetdoc@latest  && \ 
    go install honnef.co/go/tools/cmd/staticcheck@latest  && \ 
    go install golang.org/x/tools/cmd/gorename@latest  && \ 
    go install github.com/go-delve/delve/cmd/dlv@latest  && \ 
    go install github.com/766b/go-outliner@latest

ARG host_uid
ARG host_gid
ARG user

RUN groupadd -f -r --gid ${host_gid} ${user}  && \
    useradd -m -r --uid ${host_uid} --gid ${host_gid} ${user} && \
    usermod -aG ${user} ${user} && \
    echo "%${user}     ALL=(ALL)    NOPASSWD:ALL" >> /etc/sudoers.d/${user}-group    

USER ${host_uid}
SHELL ["/bin/bash", "-c"] 
