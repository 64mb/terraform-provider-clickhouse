#!/bin/sh
set -eu

cert_dir=${1:?usage: generate-certs.sh output-directory}
mkdir -p "$cert_dir"
umask 077
openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 2 \
  -subj '/CN=ClickHouse provider test CA' \
  -keyout "$cert_dir/ca.key" -out "$cert_dir/ca.pem"
openssl req -newkey rsa:2048 -nodes -sha256 \
  -subj '/CN=localhost' \
  -keyout "$cert_dir/server.key" -out "$cert_dir/server.csr"
cat > "$cert_dir/server.ext" <<'EXT'
subjectAltName=DNS:localhost,IP:127.0.0.1
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
EXT
openssl x509 -req -sha256 -days 2 -in "$cert_dir/server.csr" \
  -CA "$cert_dir/ca.pem" -CAkey "$cert_dir/ca.key" -CAcreateserial \
  -extfile "$cert_dir/server.ext" -out "$cert_dir/server.pem"
# allow the container's [clickhouse] user to read only the server credentials
chmod 755 "$cert_dir"
chmod 644 "$cert_dir/ca.pem" "$cert_dir/server.pem" "$cert_dir/server.key"
