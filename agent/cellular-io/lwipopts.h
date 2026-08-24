#ifndef MDD_CELLULAR_IO_LWIPOPTS_H
#define MDD_CELLULAR_IO_LWIPOPTS_H

#include <stdio.h>
#include <stdlib.h>

#define NO_SYS 1
#define SYS_LIGHTWEIGHT_PROT 0
#define LWIP_NETCONN 0
#define LWIP_SOCKET 0
#define LWIP_IPV4 1
#define LWIP_IPV6 0
#define LWIP_TCP 1
#define LWIP_UDP 1
#define LWIP_DNS 1
#define LWIP_ICMP 1
#define LWIP_DHCP 0
#define LWIP_AUTOIP 0
#define LWIP_IGMP 0
#define MEM_ALIGNMENT 8
#define MEM_SIZE (256 * 1024)
#define PBUF_POOL_SIZE 256
#define PBUF_POOL_BUFSIZE 1700
#define MEMP_NUM_TCP_PCB 32
#define MEMP_NUM_TCP_SEG 128
#define MEMP_NUM_UDP_PCB 32
#define TCP_SND_BUF (16 * TCP_MSS)
#define TCP_SND_QUEUELEN 64
#define TCP_WND (16 * TCP_MSS)
#define MEMP_NUM_SYS_TIMEOUT 64
#define PPP_SUPPORT 1
#define NUM_PPP 1
#define PPPOS_SUPPORT 1
#define PPPOE_SUPPORT 0
#define PPPOL2TP_SUPPORT 0
#define PPP_SERVER 0
#define PPP_IPV4_SUPPORT 1
#define PPP_IPV6_SUPPORT 0
#define PAP_SUPPORT 1
#define CHAP_SUPPORT 1
#define MSCHAP_SUPPORT 0
#define EAP_SUPPORT 0
#define CCP_SUPPORT 0
#define VJ_SUPPORT 0
#define MD5_SUPPORT 1
#define PPP_NOTIFY_PHASE 1
#define LWIP_DNS_SECURE 0
#define DNS_MAX_SERVERS 2
#define LWIP_STATS 0
#define LWIP_DEBUG 0
#define LWIP_PLATFORM_DIAG(x) do { printf x; } while (0)
#define LWIP_PLATFORM_ASSERT(x) do { fprintf(stderr, "lwIP assert: %s\n", (x)); abort(); } while (0)

#endif
