#!/bin/sh
### BEGIN INIT INFO
# Provides:          mqtt-exporter
# Required-Start:    $all
# Required-Stop:
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# X-Start-Before:
# Short-Description: mqtt-exporter
# Description:       mqtt-exporter
### END INIT INFO

EXE="./mqtt-exporter -http-addr 0.0.0.0:8080"
CHDIR="/opt"
PIDFILE="/var/run/mqtt-exporter.pid"
SCRIPT="while true;do ${EXE}; sleep 30; done"

op=$1

do_start()
{
    start-stop-daemon --start --quiet --oknodo --make-pidfile --background --pidfile "${PIDFILE}" --chdir "${CHDIR}" --exec /bin/bash -- -c "${SCRIPT}"
    return $?
}

do_stop()
{
    start-stop-daemon --stop --quiet --oknodo --retry TERM/5/KILL/5 --pidfile "${PIDFILE}"
    pkill mqtt-exporter
    return $?
}

case "$op" in
  start)
    do_start
    exit $?
    ;;
  stop)
    do_stop
    exit $?
    ;;
  restart)
    pkill mqtt-exporter
    exit $?
    ;;
  status)
    start-stop-daemon --status --pidfile "${PIDFILE}"
    exit $?
    ;;
  *)
    echo "Usage: $0 [start|stop]" >&2
    exit 3
    ;;
esac

exit 0
