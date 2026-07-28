# piccolo-perf RouterOS Container Setup
# RouterOS 7.6+ required. Tested on RB4011, CCR2004, RB5009 (ARM/ARM64).
#
# Prerequisites:
#   1. Enable container mode: /system/package/enable container  (requires reboot)
#   2. Ensure the router has internet access to pull the image
#   3. Set PICCOLO_CONFIG_URL below to your config server URL
#
# Usage: paste into RouterOS terminal or import via /import file=setup.rsc

# ── Configuration — edit these values ────────────────────────────────────────

:local configUrl    "https://piccolo-perf.example.com:8443/piccolo-perf-config.json"
:local imageTag     "ghcr.io/retecolo/piccolo-perf:latest"
:local vethName     "veth-piccolo"
:local bridgeName   "br-piccolo"
:local containerName "piccolo-perf"

# IPv4 address for the container (pick an unused /30 from an RFC1918 range).
# The router side gets .1, container gets .2.
:local v4Network    "172.31.255.0/30"
:local v4Router     "172.31.255.1"
:local v4Container  "172.31.255.2"

# IPv6: assign a GUA or ULA prefix the container will use.
# The container will use SLAAC from the bridge if RA is configured, or set a
# static address here. Leave empty to rely on SLAAC only.
:local v6Address    ""
# e.g. "2001:db8:1:2::probe/64" — must be routable from other fleet probes

# Ports to forward from the router's WAN/mesh interface to the container.
# Adjust the in-interface to match your uplink (e.g. ether1, sfp-sfpplus1).
:local inInterface  "ether1"

# ── 1. Create a veth pair and bridge ─────────────────────────────────────────

/interface/veth/add name=$vethName address=$v4Container gateway=$v4Router

/interface/bridge/add name=$bridgeName
/interface/bridge/port/add bridge=$bridgeName interface=$vethName

/ip/address/add address=$v4Router interface=$bridgeName

# IPv6 on the bridge (router side) — enables RA so the container gets a GUA
# via SLAAC if v6Address is left empty.
/ipv6/address/add interface=$bridgeName advertise=yes from-pool=your-prefix-pool-name
# If you have a static prefix, replace the above with:
# /ipv6/address/add address=2001:db8:1:2::1/64 interface=$bridgeName advertise=yes

# ── 2. NAT for IPv4 outbound (container → internet for config fetch) ─────────

/ip/firewall/nat/add chain=srcnat src-address=$v4Network \
    action=masquerade comment="piccolo-perf container outbound"

# ── 3. Firewall — allow fleet ports inbound to container ─────────────────────
# TWAMP reflector
/ip/firewall/filter/add chain=forward in-interface=$inInterface \
    protocol=udp dst-port=862 action=accept \
    comment="piccolo-perf TWAMP"
/ipv6/firewall/filter/add chain=forward in-interface=$inInterface \
    protocol=udp dst-port=862 action=accept \
    comment="piccolo-perf TWAMP IPv6"

# Bandwidth sink
/ip/firewall/filter/add chain=forward in-interface=$inInterface \
    protocol=tcp dst-port=5201 action=accept \
    comment="piccolo-perf BwServer"
/ipv6/firewall/filter/add chain=forward in-interface=$inInterface \
    protocol=tcp dst-port=5201 action=accept \
    comment="piccolo-perf BwServer IPv6"

# Prometheus metrics
/ip/firewall/filter/add chain=forward in-interface=$inInterface \
    protocol=tcp dst-port=9862 action=accept \
    comment="piccolo-perf metrics"
/ipv6/firewall/filter/add chain=forward in-interface=$inInterface \
    protocol=tcp dst-port=9862 action=accept \
    comment="piccolo-perf metrics IPv6"

# ── 4. DNAT — forward fleet ports from router IP to container ────────────────
# Only needed if the container has only a private IPv4. Skip if using GUA IPv6
# and Prometheus scrapes the container's IPv6 directly.

/ip/firewall/nat/add chain=dstnat in-interface=$inInterface \
    protocol=udp dst-port=862 \
    action=dst-nat to-addresses=$v4Container to-ports=862 \
    comment="piccolo-perf TWAMP DNAT"

/ip/firewall/nat/add chain=dstnat in-interface=$inInterface \
    protocol=tcp dst-port=5201 \
    action=dst-nat to-addresses=$v4Container to-ports=5201 \
    comment="piccolo-perf BwServer DNAT"

/ip/firewall/nat/add chain=dstnat in-interface=$inInterface \
    protocol=tcp dst-port=9862 \
    action=dst-nat to-addresses=$v4Container to-ports=9862 \
    comment="piccolo-perf metrics DNAT"

# ── 5. Container environment ──────────────────────────────────────────────────

/container/envs/add name=piccolo-env key=PICCOLO_CONFIG_URL value=$configUrl
/container/envs/add name=piccolo-env key=PICCOLO_MODE      value="exporter"
/container/envs/add name=piccolo-env key=PICCOLO_PROBE_MODE value="background"
/container/envs/add name=piccolo-env key=PICCOLO_METRICS_ADDR value="[::]:9862"

# ── 6. Create and start the container ────────────────────────────────────────

/container/add \
    name=$containerName \
    image=$imageTag \
    interface=$vethName \
    envlist=piccolo-env \
    root-dir=disk1/piccolo-perf \
    hostname=$containerName \
    logging=yes \
    start-on-boot=yes \
    cap-add=NET_RAW,NET_BIND_SERVICE

/container/start $containerName

:log info "piccolo-perf container started. Check status: /container/print"
:log info "Logs: /container/shell $containerName  or  /log/print where topics~\"container\""
