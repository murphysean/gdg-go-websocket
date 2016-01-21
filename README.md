#Welcome to the GDG Go Hacknight Websocket Server 

Introduction
---

For the Jan 2016 GDG Monthly we talked about websockets. I shared
this presentation: [Google Slide](https://docs.google.com/presentation/d/1dVGIKJhQ-Fj1C1AzS5CBAcKWxNwnclDnDD_re2rRZHA/edit?usp=sharing)

There is a default webpage that can be found @ localhost:8080/.
This app will connect to a websocket found at

	localhost:8080/ws/{connectionName}

The websocket api has a few features:

### Shared Objects

Shared objects allow you to share state between connected clients.
This is done in the form of a JSON document. Send a message of the form:

	{
		"source":"client",
		"command": "patch"|"merge"|"subscribe"|"delete",
		"path": "unique-path-string",
		"message": JSON Document
	}

Note that the object must be created via the patch or merge commands
before it can be subscribed to.

### Publish

Publish allows you to fire (and forget) a message that will be distributed
to all subscribers. The message will not be retained after it is copied
to the subscribers. It's kinda like an irc chat channel.

	{
		"source":"client",
		"command": "notify",
		"path": "unique-path-string",
		"message": JSON Document
	}


### Notify

Notify allows you to publish a message direcly to another connected user.

	{
		"source":"client",
		"command": "notify",
		"path": "other-users-id",
		"message": JSON Document
	}

Hope this document helps.
