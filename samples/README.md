# Fresh Breath Samples

These are standalone apps that can be used to test your install or get a sense
of how Fresh Breath works.

**BUT FIRST:** Yeah, you'll need to replace the `frbr.js` URL in the HTML files
to reflect your personal server URL and an app nonce.

```js
import { login, ServiceProxy } from "https://localhost:9009/frbr.js?[nonce]";
```

To do this, create an app in the control panel for this sample, then give the
app access to the integrations it needs.

- mcp: This sample uses Notion's MCP. You'll need to set up a service in the
  control panel for https://mcp.notion.com/mcp

- ssh: This sample use Fresh Breath's basic SSH setup in order to log a user in
  and then to connect and transfer files to a server. You'll need to:

  - Give the app access to the SSH integration in the control panel.
  - Create SSH credentials for yourself - can be done on the Settings page.
  - Copy the public key generated into the `~/.ssh/authorized_keys` file on the
    server you want to connect to.
  - Open the ssh.html file in a browser and log in with the passphrase you set
    up when you created your SSH credentials.

These are designed to be small samples, just to get a quick look at how the
thing works - not to be used as apps you install and rely on.
