package main
import("encoding/binary";"fmt";"net";"syscall";"time")
func main(){
 iface,err:=net.InterfaceByName("eth0");if err!=nil{panic(err)}
 _,network,_:=net.ParseCIDR("10.99.0.0/24")
 addresses,err:=iface.Addrs();if err!=nil{panic(err)}
 var ip net.IP
 for _,address:=range addresses{candidate,_,e:=net.ParseCIDR(address.String());if e==nil && candidate.To4()!=nil && network.Contains(candidate){if ip!=nil{panic("multiple competition addresses")};ip=candidate.To4()}}
 if ip==nil{panic("competition address not configured")}
 fd,err:=syscall.Socket(syscall.AF_PACKET,syscall.SOCK_RAW,0x0608);if err!=nil{panic(err)};defer syscall.Close(fd)
 addr:=&syscall.SockaddrLinklayer{Ifindex:iface.Index,Protocol:0x0608,Halen:6,Addr:[8]uint8{255,255,255,255,255,255}}
 for i:=0;i<3;i++{for _,op:=range []uint16{1,2}{
 b:=make([]byte,42);for j:=0;j<6;j++{b[j]=255};copy(b[6:12],iface.HardwareAddr);binary.BigEndian.PutUint16(b[12:14],0x0806)
 binary.BigEndian.PutUint16(b[14:16],1);binary.BigEndian.PutUint16(b[16:18],0x0800);b[18]=6;b[19]=4;binary.BigEndian.PutUint16(b[20:22],op);copy(b[22:28],iface.HardwareAddr);copy(b[28:32],ip);if op==2{for j:=32;j<38;j++{b[j]=255}};copy(b[38:42],ip)
 if err=syscall.Sendto(fd,b,0,addr);err!=nil{panic(err)}
 };time.Sleep(200*time.Millisecond)}
 fmt.Printf("Announced %s at %s\n",ip,iface.HardwareAddr)
}
