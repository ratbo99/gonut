# gonut - upsmonitor
This is a Nut-Client written in Go for Windows to Shutdown a PC when an UPS is on Batteries.
I only tested it with a Unifi UPS 2U, because i can't get another Windows Nut-Client to run with the Unifi NUT Server.

## Usage
Start upsmonitor.go or upsmonitor.exe. After the that edit the created config.json file and start it again.

```
  "host": "192.168.1.50", //UPS/NUT Server IP
  "port": 3493,  // NUT PORT
  "user": "", //empty when no authentification
  "password": "",
  "upsName": "ups", //UPS Name
  "pollInterval": 10000, //interval to check the ups in milliseconds
  "shutdownDelay": 120000, //delay until computer shutsdown after the battery went offline
  "enableNotifications": true,
  "autostart": true 
```

you will find the app in the systemtray.

## Todo
- more testing
- gui to set configuration
- translation
