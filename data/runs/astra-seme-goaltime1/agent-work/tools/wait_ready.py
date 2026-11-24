from server_address import API_ADDRESS
import http.client,time,sys
end=time.monotonic()+60
while True:
 try:
  c=http.client.HTTPConnection(API_ADDRESS,8080,timeout=2);c.request('GET','/api/payment');r=c.getresponse();b=r.read();c.close()
  if r.status==200:print('API ready',b.decode().strip());break
 except OSError:pass
 if time.monotonic()>end:sys.exit('API readiness timeout')
 time.sleep(1)
