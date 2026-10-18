import CoreVideo
import Foundation
import Testing
@preconcurrency import WebRTC

@testable import Linx

// The small window a video call goes on in over another app (owner,
// 2026-10-18, `CallPictureInPicture`). It draws with a sample-buffer layer,
// so a picture WebRTC decoded into plain planes (AV1 does) is copied into
// the layer's two-plane form first: that copy has to put every value in its
// place, or the other person shows up green.

struct SmallWindowTests {
    @Test func aDecodedPictureIsCopiedIntoTheWindowsFormExactly() throws {
        let width = 6
        let height = 4
        let planes = RTCMutableI420Buffer(width: Int32(width), height: Int32(height))
        for row in 0..<height {
            for column in 0..<width {
                planes.mutableDataY[row * Int(planes.strideY) + column] = UInt8(row * 10 + column)
            }
        }
        for row in 0..<height / 2 {
            for column in 0..<width / 2 {
                planes.mutableDataU[row * Int(planes.strideU) + column] = UInt8(100 + row * 10 + column)
                planes.mutableDataV[row * Int(planes.strideV) + column] = UInt8(200 + row * 10 + column)
            }
        }
        var pool: CVPixelBufferPool?
        var size = (0, 0)
        let out = try #require(SampleBufferFeed.copy(planes, into: &pool, size: &size))
        #expect(CVPixelBufferGetWidth(out) == width)
        #expect(CVPixelBufferGetHeight(out) == height)
        #expect(CVPixelBufferGetPixelFormatType(out) == kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange)
        CVPixelBufferLockBaseAddress(out, .readOnly)
        defer { CVPixelBufferUnlockBaseAddress(out, .readOnly) }
        let luma = try #require(CVPixelBufferGetBaseAddressOfPlane(out, 0)?.assumingMemoryBound(to: UInt8.self))
        let chroma = try #require(CVPixelBufferGetBaseAddressOfPlane(out, 1)?.assumingMemoryBound(to: UInt8.self))
        let lumaStride = CVPixelBufferGetBytesPerRowOfPlane(out, 0)
        let chromaStride = CVPixelBufferGetBytesPerRowOfPlane(out, 1)
        for row in 0..<height {
            for column in 0..<width {
                #expect(luma[row * lumaStride + column] == UInt8(row * 10 + column))
            }
        }
        for row in 0..<height / 2 {
            for column in 0..<width / 2 {
                #expect(chroma[row * chromaStride + 2 * column] == UInt8(100 + row * 10 + column))
                #expect(chroma[row * chromaStride + 2 * column + 1] == UInt8(200 + row * 10 + column))
            }
        }
        // The next picture the same size comes from the same pool.
        let again = try #require(SampleBufferFeed.copy(planes, into: &pool, size: &size))
        #expect(CVPixelBufferGetWidth(again) == width)
    }

    @Test @MainActor func theCameraMayKeepGoingWhileAnotherAppIsInFront() {
        let camera = Camera(factory: RTCPeerConnectionFactory())
        #expect(camera.keepsGoingInTheSmallWindow)
    }
}
