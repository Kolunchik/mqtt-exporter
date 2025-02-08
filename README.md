# mqtt-exporter
Экспортёр и шаблон для Zabbix

В последних версиях Zabbix произошли приятные изменения, которые позволили упростить мониторинг множества устройств WirenBoard.

По историческим причинам (есть десятки устаревших устройств :) мы не можем использовать встроенную в агент возможность подключаться к mqtt, поэтому вынуждены использовать примитивный экспортёр данных и выгребать данные из него. Много лет эксплуатации такого чуда показали его работоспособность, на удивление.

Мы обычно экспортёр устанавливаем и запускаем непосредственно на WirenBoard. Проверено и работает на WB4, WB5 и WB6.
Но есть места, где десятки копий запущены на мини-компьютере, где крутиться заодно Zabbix proxy. И так тоже нормально.
В этом случае нужно просто менять http-порт и адрес брокера для каждой копии.

На WB4 используется mqtt-exporter.sh
На WB5 и WB6 используется mqtt-exporter.service

Для LLD используется обычный zabbix_sender (пример ниже)

```zabbix_sender --config /etc/zabbix/zabbix_agentd.conf -v -k wbio-di-wd-14.lld -o '[{"{#DEVICE}":"EXT1"}]'
zabbix_sender --config /etc/zabbix/zabbix_agentd.conf -v -k wb-w1.lld -o '[{"{#DEVICE}":"28-0315539a30ff"},{"{#DEVICE}":"28-0315906bf5ff"},{"{#DEVICE}":"28-04159043b9ff"}]'
zabbix_sender --config /etc/zabbix/zabbix_agentd.conf -v -k wb-map3e.lld -o '[{"{#DEVICE}":"wb-map3e_233"}]'
zabbix_sender --config /etc/zabbix/zabbix_agentd.conf -v -k msu24hit.lld -o '[{"{#DEVICE}":"msu24hit_5"}]'
zabbix_sender --config /etc/zabbix/zabbix_agentd.conf -v -k wb-msw2.lld -o '[{"{#DEVICE}":"wb-msw2_3"}]'
zabbix_sender --config /etc/zabbix/zabbix_agentd.conf -v -k wb-ms-thls.lld -o '[{"{#DEVICE}":"wb-ms-thls_2"}]'
zabbix_sender --config /etc/zabbix/zabbix_agentd.conf -v -k wb-mr3.lld -o '[{"{#DEVICE}":"wb-mr3lv_25"}]'
zabbix_sender --config /etc/zabbix/zabbix_agentd.conf -v -k wb-mr11.lld -o '[{"{#DEVICE}":"wb-mr11_190"}]'
zabbix_sender --config /etc/zabbix/zabbix_agentd.conf -v -k wb-mrgb.lld -o '[{"{#DEVICE}":"wb-mrgb_4"}]'
zabbix_sender --config /etc/zabbix/zabbix_agentd.conf -v -k wb-mrm2.lld -o '[{"{#DEVICE}":"wb-mrm2_21"}]'```
