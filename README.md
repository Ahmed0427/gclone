# gclone

A minimal Git clone, written from scratch in Go.  
Because understanding Git beats just using it.

### What Is This?

**gclone** is a stripped-down reimplementation of `git clone`.  
It talks directly to a Git server, downloads the repository’s data, and builds a working `.git` directory without relying on Git itself.

### How It Works

1.  Contacts the remote server to discover available refs (Works over HTTP/HTTPS)
2.  Downloads the packfile (Git’s compressed bundle of objects)
3.  Unpacks the objects into their raw form 
4.  Resolves deltas to reconstruct original content
5.  Writes everything into a minimal `.git` directory
6.  Checks out the working tree

### Usage

```bash
go run main.go <repo_url> <target_folder>
```

Example:
```bash
go run main.go https://github.com/user/repo ./my-repo
```

### Why?

Don’t really know.  
It felt like it would be cool, and I learned a lot about Git internals along the way.


### Should You Use It?

No.
But if you want to really understand some about Git internals, this is for you.  
Read the code, experiment, break it, rebuild it.

### Resources

All the resources I used will be here: [https://app.codecrafters.io/courses/git/stages/mg6](https://app.codecrafters.io/courses/git/stages/mg6)

### License

MIT. Fork it, learn from it, improve it.
