# Creates the Linx internal certificate authority (ADR-011) in the
# linx-step-ca volume. linx setup runs this once, in a throwaway step-ca
# container with no network.
#
# The root key is generated already encrypted with the owner's backup
# passphrase, in /tmp (a RAM-only tmpfs). Its only copy leaves through /backup;
# the volume keeps the root certificate but never the root key.
set -euo pipefail
cd /home/step
if [ -e config/ca.json ]; then
  echo "The internal certificate authority already exists; setup won't replace it." >&2
  exit 1
fi
# Clear anything an interrupted earlier attempt left behind.
rm -rf config certs secrets db templates
umask 077

passphrase() { printf %s "$LINX_ROOT_BACKUP_PASSPHRASE"; }
ca_config=/home/step/config/ca.json

step certificate create "Linx Internal Root CA" /tmp/root_ca.crt /tmp/root_ca_key \
  --profile root-ca --kty EC --crv P-256 --not-after 87600h \
  --password-file <(passphrase)

step ca init --deployment-type standalone --name "Linx Internal CA" \
  --dns linx-step-ca --dns step-ca --dns localhost --address :9000 \
  --root /tmp/root_ca.crt --key /tmp/root_ca_key --key-password-file <(passphrase) \
  --password-file /run/secrets/linx_step_ca_password \
  --provisioner linx-services \
  --provisioner-password-file /run/secrets/linx_ca_services_password

# Service certificates: 24 hours, renewed at two-thirds of their lifetime.
step ca provisioner update linx-services --ca-config "$ca_config" \
  --x509-min-dur 5m --x509-max-dur 24h --x509-default-dur 24h --ssh=false

# Device certificates: lifetime equals the inactivity window (7 days). Only
# the control plane gets this provisioner's password.
step ca provisioner add linx-devices --ca-config "$ca_config" --type JWK --create \
  --password-file /run/secrets/linx_ca_devices_password \
  --x509-min-dur 5m --x509-max-dur 168h --x509-default-dur 168h --ssh=false

test ! -e secrets/root_ca_key
cp /tmp/root_ca.crt /tmp/root_ca_key /backup/
