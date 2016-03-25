#!/bin/bash
export GOPATH=/home/vagrant/go
export PATH=$PATH:/usr/local/go/bin:$GOPATH/bin

apt-get update
apt-get -y install git

# Install Go to /usr/local
curl -O https://storage.googleapis.com/golang/go1.5.3.linux-amd64.tar.gz
tar -C /usr/local -xzf go1.5.3.linux-amd64.tar.gz

# link our repository to the path GO is expecting
mkdir -p /home/vagrant/go/src/github.com/ipfs
ln -s /vagrant /home/vagrant/go/src/github.com/ipfs/go-ipfs

# setup our GO environment variables
echo "export GOPATH=/home/vagrant/go" >> /home/vagrant/.profile
echo "export PATH=$PATH:/usr/local/go/bin:$GOPATH/bin" >> /home/vagrant/.profile

# install gx and gx-go, then compile and install go-ipfs to our local system
cd /home/vagrant/go/src/github.com/ipfs/go-ipfs
make toolkit_upgrade
make install

# make sure our vagrant user owns everything it should
chown -R vagrant:vagrant /home/vagrant
