# lfs-cache

lfs-cache is a caching proxy for [Git LFS](https://git-lfs.github.com/) servers.

## Usage

#### Docker

```
$ docker run --name lfscache --rm -d -v /my/cache/dir/lfs:/lfs saracen/lfscache:latest --url github.com/org/repo.git/info/lfs --http-addr :80  --directory /lfs
```

#### Binary

Download the correct [binary](https://github.com/saracen/lfscache/releases) for your system.

```
$ ./lfscache --url github.com/org/repo.git/info/lfs --directory /my/cache/dir/lfs --http-addr=:9876
```

`--directory` specifies the cache directory. The layout is the same used by the
Git LFS client, so it might be a good idea to copy over your `.git/lfs/objects`
directory to preload the cache (`cp -r .git/lfs/objects /my/cache/dir/lfs`).
The `tmp` and `incomplete` directories do not need to be copied over.

Now you need to have your Git LFS client point to the proxy. There are several
ways to do this. The easiest method is changing the lfs url that will be used
in your local git config:
```
# note that repo.git/info/lfs is not required
git config lfs.url http://localhost:9876/

# you can confirm the Endpoint that will be used by running
git lfs env | grep Endpoint
```
