#The ID can be found via API or the terminal
#The command for the terminal is -> :put [/ip/dns/forwarders get [print show-ids]]
terraform import routeros_ip_dns_forwarders.test *3
#Or you can import a resource using one of its attributes
terraform import routeros_ip_dns_forwarders.test "name=xxx"