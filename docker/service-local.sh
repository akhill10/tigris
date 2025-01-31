#!/bin/bash

function start_typesense() {
	/usr/bin/typesense-server --config=/etc/typesense/typesense-server.ini &
}

function wait_for_typesense() {
	echo "Waiting for typesense to start"
	IS_LEADER=1
	echo "Waiting for typesense to become leader"
  while [ ${IS_LEADER} -ne 0 ]
  do
		curl -H "X-TYPESENSE-API-KEY: ${TIGRIS_SERVER_SEARCH_AUTH_KEY}" localhost:8108/status | grep LEADER
		IS_LEADER=$?
		if [ ${IS_LEADER} -ne 0 ]
		then
			echo "Typesense is not leader yet, waiting"
		fi
		sleep 2
	done

	echo "Waiting for typesense to respond to list collections"
	LIST_COLLECTIONS_RESP=1
	while [ ${LIST_COLLECTIONS_RESP} -ne 0 ]
	do
		# Try to do list collections and see the response code, this can take time
		curl -H "X-TYPESENSE-API-KEY: ${TIGRIS_SERVER_SEARCH_AUTH_KEY}" -I -X GET localhost:8108/collections | grep "200 OK"
		LIST_COLLECTIONS_RESP=$?
		if [ ${LIST_COLLECTIONS_RESP} -ne 0 ]
		then
			echo "Typesense could not list collections yet, waiting"
		fi
		sleep 2
	done
}

function start_fdb() {
	fdbserver --listen-address 127.0.0.1:4500 --public-address 127.0.0.1:4500 --datadir /var/lib/foundationdb/data --logdir /var/lib/foundationdb/logs --locality-zoneid tigris --locality-machineid tigris &
	fdbcli --exec 'configure new single memory'
}

function wait_for_fdb() {
	echo "Waiting for foundationdb to start"
	OUTPUT="something_else"
	while [ "x${OUTPUT}" != "xThe database is available." ]
	do
		OUTPUT=$(fdbcli --exec 'status minimal')
		sleep 2
	done
}

export TIGRIS_SERVER_SERVER_TYPE=database
export TIGRIS_SERVER_SEARCH_AUTH_KEY=ts_dev_key
export TIGRIS_SERVER_SEARCH_HOST=localhost
export TIGRIS_SERVER_CDC_ENABLED=true

start_fdb
wait_for_fdb
start_typesense
wait_for_typesense
/server/service
