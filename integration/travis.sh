#!/usr/bin/env bash

set -ex

sudo groupadd nobody
sudo useradd hello

sudo /sbin/stop runsvdir
sudo sed -i -e 's;/usr/sbin/runsvdir-start;/usr/bin/runsvdir /var/service;g' /etc/init/runsvdir.conf
sudo /sbin/start runsvdir
sudo mkdir -p /etc/servicebuilder.d /var/service-stage /var/service

sudo cp $GOPATH/bin/p2-exec /usr/local/bin

# make ssl certs
subj="
C=US
ST=CA
O=SQ
localityName=SF
commonName=$HOSTNAME
organizationalUnitName=Vel
emailAddress=doesntmatter@something.edu
"
CERTPATH=/var/tmp/certs
mkdir -p $CERTPATH
openssl req -x509 -newkey rsa:2048 -keyout $CERTPATH/key.pem -out $CERTPATH/cert.pem -nodes -days 300 -subj "$(echo -n "$subj" | tr "\n" "/")"

sudo wget https://dl.bintray.com/mitchellh/consul/0.5.2_linux_amd64.zip
sudo unzip 0.5.2_linux_amd64.zip
sudo mv consul /usr/bin/
sudo env PATH=$PATH GOPATH=$GOPATH GOROOT=$GOROOT go run integration/single-node-slug-deploy/check.go
