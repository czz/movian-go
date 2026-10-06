

exports.writeFileSync = function(filename, data, opts) {
  var fs = require('native/fs');
  var fd = fs.open(filename, 'w');
  try {
    fs.write(fd, data, 0, null, 0);
  }
  finally {
    Core.resourceDestroy(fd);
  }
}

exports.readFileSync = function(filename, opts) {
  var fs = require('native/fs');
  var fd = fs.open(filename, 'r');
  try {
    var buf = Uint8Array.allocPlain(fs.fsize(fd));
    fs.read(fd, buf.valueOf(), 0, buf.length, 0);
    return new TextDecoder().decode(buf);
  } finally {
    Core.resourceDestroy(fd);
  }
}
