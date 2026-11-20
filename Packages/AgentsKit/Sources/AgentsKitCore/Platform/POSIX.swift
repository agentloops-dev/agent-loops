#if canImport(Darwin)
import Darwin
#elseif canImport(Musl)
import Musl
#elseif canImport(Glibc)
import Glibc
#endif

/// The C library, named once (037).
///
/// The daemon calls `read`, `write`, `close`, `connect` and `kill` by module name so a
/// method of the same name nearby can never be picked instead. That module is `Darwin`
/// on the Mac and `Musl` in the static Linux build of `agentsd`, so the name lives here
/// and nowhere else.
public enum POSIX {
    @inline(__always)
    public static func read(_ fd: Int32, _ buffer: UnsafeMutableRawPointer?, _ count: Int) -> Int {
        #if canImport(Darwin)
        Darwin.read(fd, buffer, count)
        #elseif canImport(Musl)
        Musl.read(fd, buffer, count)
        #else
        Glibc.read(fd, buffer, count)
        #endif
    }

    @inline(__always)
    public static func write(_ fd: Int32, _ buffer: UnsafeRawPointer?, _ count: Int) -> Int {
        #if canImport(Darwin)
        Darwin.write(fd, buffer, count)
        #elseif canImport(Musl)
        Musl.write(fd, buffer, count)
        #else
        Glibc.write(fd, buffer, count)
        #endif
    }

    @inline(__always) @discardableResult
    public static func close(_ fd: Int32) -> Int32 {
        #if canImport(Darwin)
        Darwin.close(fd)
        #elseif canImport(Musl)
        Musl.close(fd)
        #else
        Glibc.close(fd)
        #endif
    }

    @inline(__always)
    public static func connect(_ fd: Int32, _ address: UnsafePointer<sockaddr>, _ length: socklen_t) -> Int32 {
        #if canImport(Darwin)
        Darwin.connect(fd, address, length)
        #elseif canImport(Musl)
        Musl.connect(fd, address, length)
        #else
        Glibc.connect(fd, address, length)
        #endif
    }
}
