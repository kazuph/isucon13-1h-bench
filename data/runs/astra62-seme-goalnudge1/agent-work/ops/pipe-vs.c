/* Configure the public HTTPS virtual service on the contest's three nodes. */
#include <arpa/inet.h>
#include <errno.h>
#include <linux/ip_vs.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

int main(int argc, char **argv) {
    struct {
        struct ip_vs_service_user service;
        struct ip_vs_dest_user dest;
    } rule = {0};
    if (argc != 2 && argc != 4) return 2;
    int sock = socket(AF_INET, SOCK_RAW, IPPROTO_RAW);
    if (sock < 0) { perror("socket"); return 1; }
    rule.service.addr = inet_addr("10.99.0.11");
    rule.service.protocol = IPPROTO_TCP;
    rule.service.port = htons(443);
    rule.service.netmask = htonl(0xffffffff);
    strcpy(rule.service.sched_name, "wrr");
    if (argc == 2) {
        if (strcmp(argv[1], "off") != 0) return 2;
        if (setsockopt(sock, IPPROTO_IP, IP_VS_SO_SET_DEL, &rule.service, sizeof rule.service) && errno != ESRCH) {
            perror("remove service"); return 1;
        }
        return 0;
    }
    if (setsockopt(sock, IPPROTO_IP, IP_VS_SO_SET_ADD, &rule.service, sizeof rule.service) && errno != EEXIST) {
        perror("add service"); return 1;
    }
    const char *nodes[] = {"10.99.0.11","10.99.0.12","10.99.0.13"};
    for (int i=0; i<3; i++) {
        int weight = atoi(argv[i+1]);
        if (weight < 0 || weight > 100) return 2;
        rule.dest.addr = inet_addr(nodes[i]);
        rule.dest.port = rule.service.port;
        rule.dest.weight = weight;
        rule.dest.conn_flags = IP_VS_CONN_F_DROUTE;
        if (setsockopt(sock, IPPROTO_IP, IP_VS_SO_SET_ADDDEST, &rule, sizeof rule)) {
            if (errno != EEXIST || setsockopt(sock, IPPROTO_IP, IP_VS_SO_SET_EDITDEST, &rule, sizeof rule)) {
                perror("set destination"); return 1;
            }
        }
    }
    return close(sock) != 0;
}
