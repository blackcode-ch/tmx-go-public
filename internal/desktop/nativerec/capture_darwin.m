//go:build desktop && cgo

// Native screen recording for the macOS app (DESIGN.md "Recorder", native capture), with
// ScreenCaptureKit:
//   - the system picker (SCContentSharingPicker) for a display, a window or an app;
//   - one SCStream on what the owner picked, with device audio and the microphone;
//   - an SCRecordingOutput per segment, which writes the MP4 (H.264 + AAC; the microphone and
//     device audio mixed into one track, which is what every player plays);
//   - the stream's sound, mixed and resampled to 16 kHz mono for the segment's WAV;
//   - the newest frame, as a JPEG when Go asks for one.
//
// ScreenCaptureKit is weak-linked and everything here needs macOS 15 (SCRecordingOutput and
// captureMicrophone). Older systems report "not supported" and the window uses the WebView
// recorder.
//
// Threads: Go calls the tmxrec* functions one at a time (nativerec's op lock). The stream
// delivers frames and sound on one serial queue (q), and the mixer lives on it. The picker runs
// on the main queue. Sound, the picker's answer and a stream that stopped reach Go through the
// exported tmxrecGo* functions.

#import <Foundation/Foundation.h>
#import <AVFoundation/AVFoundation.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#import <ImageIO/ImageIO.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#include <mach/mach_time.h>
#include <os/lock.h>
#include "_cgo_export.h"

#if !__has_feature(objc_arc)
#error "capture_darwin.m needs ARC (-fobjc-arc)"
#endif

#define RATE 16000        // the WAV's sample rate
#define ACC (RATE * 8)    // the mixer's room: 8 s
#define MAX_JPEG (600 * 1024)

static char *dupText(NSString *s) { return s.length ? strdup(s.UTF8String) : NULL; }

// errorText is what the owner reads when the capture fails; denied permissions say where to allow them.
static NSString *errorText(NSError *e) {
    if (!e) return @"the capture failed";
    if ([e.domain isEqualToString:@"com.apple.ScreenCaptureKit.SCStreamErrorDomain"] || [e.domain hasSuffix:@"SCStreamErrorDomain"]) {
        switch (e.code) {
        case -3801: // SCStreamErrorUserDeclined
            return @"Screen recording isn't allowed for tmx. Allow it in System Settings → Privacy & Security → "
                   @"Screen & System Audio Recording, then try again.";
        case -3820: // SCStreamErrorFailedToStartMicrophoneCapture
            return @"The microphone did not start. If it isn't allowed: System Settings → Privacy & Security → Microphone.";
        case -3803: // SCStreamErrorMissingEntitlements
            return @"macOS refused the capture for this build of tmx (an entitlement is missing).";
        }
    }
    return e.localizedDescription ?: @"the capture failed";
}

static double hostSeconds(uint64_t ticks) {
    static mach_timebase_info_data_t tb;
    if (tb.denom == 0) mach_timebase_info(&tb);
    return (double)ticks * tb.numer / tb.denom / 1e9;
}

// ---------------------------------------------------------------------------------------------
// One source of sound (device audio or the microphone) on its way to 16 kHz mono.

@interface TMXSource : NSObject
@property(nonatomic) AudioStreamBasicDescription asbd; // the format the converter was made for
@property(nonatomic, strong) AVAudioFormat *inFormat;
@property(nonatomic, strong) AVAudioConverter *conv;
@property(nonatomic) int64_t next;     // where its next sample goes in the segment (16 kHz samples)
@property(nonatomic) BOOL started;
@property(nonatomic) uint64_t heardAt; // mach_absolute_time of its last buffer
@end

@implementation TMXSource
@end

// ---------------------------------------------------------------------------------------------
// One segment's recording output and what its delegate heard.

API_AVAILABLE(macos(15.0))
@interface TMXOut : NSObject <SCRecordingOutputDelegate>
@property(nonatomic, strong) SCRecordingOutput *output;
@property(nonatomic, strong) dispatch_semaphore_t started, finished;
@property(atomic, strong) NSError *error;
@property(atomic) BOOL didFinish, ending;
@end

@implementation TMXOut
- (void)recordingOutputDidStartRecording:(SCRecordingOutput *)o {
    dispatch_semaphore_signal(self.started);
}
- (void)recordingOutput:(SCRecordingOutput *)o didFailWithError:(NSError *)e {
    self.error = e;
    BOOL ending = self.ending;
    self.didFinish = YES;
    dispatch_semaphore_signal(self.started);
    dispatch_semaphore_signal(self.finished);
    if (!ending) { // mid-segment (a full disk): the recording stops
        char *msg = dupText([@"the recording's file failed: " stringByAppendingString:errorText(e)]);
        tmxrecGoStopped(e.code, msg);
        free(msg);
    }
}
- (void)recordingOutputDidFinishRecording:(SCRecordingOutput *)o {
    self.didFinish = YES;
    dispatch_semaphore_signal(self.finished);
}
@end

// ---------------------------------------------------------------------------------------------
// The capture.

API_AVAILABLE(macos(15.0))
@interface TMXRec : NSObject <SCContentSharingPickerObserver, SCStreamOutput, SCStreamDelegate>
@end

@implementation TMXRec {
    dispatch_queue_t _q;
    SCContentFilter *_filter; // what the owner picked (under @synchronized)
    BOOL _picking, _observing, _closing;
    SCStream *_stream;
    TMXOut *_seg;
    // the mixer (on _q)
    AVAudioFormat *_out16;
    TMXSource *_sys, *_mic;
    float *_acc;
    int16_t *_chunk;
    int64_t _base, _hi; // _acc[0] is sample _base of the segment; _hi: one past the last written
    CMTime _t0;
    BOOL _mixing;
    // the newest frame
    os_unfair_lock _frameLock;
    CVPixelBufferRef _latest;
}

- (instancetype)init {
    if ((self = [super init])) {
        _q = dispatch_queue_create("tmx.native-rec", DISPATCH_QUEUE_SERIAL);
        _out16 = [[AVAudioFormat alloc] initWithCommonFormat:AVAudioPCMFormatFloat32 sampleRate:RATE channels:1 interleaved:NO];
        _acc = calloc(ACC, sizeof(float));
        _chunk = calloc(RATE, sizeof(int16_t));
        _frameLock = OS_UNFAIR_LOCK_INIT;
    }
    return self;
}

// ---- the picker (main queue)

- (void)pick {
    @synchronized(self) {
        _filter = nil;
        _picking = YES;
    }
    dispatch_async(dispatch_get_main_queue(), ^{
        SCContentSharingPicker *p = SCContentSharingPicker.sharedPicker;
        SCContentSharingPickerConfiguration *c = [SCContentSharingPickerConfiguration new];
        c.allowedPickerModes = SCContentSharingPickerModeSingleDisplay | SCContentSharingPickerModeSingleWindow |
                               SCContentSharingPickerModeSingleApplication;
        c.allowsChangingSelectedContent = NO; // the video's size is set at the start
        p.defaultConfiguration = c;
        if (!self->_observing) {
            [p addObserver:self];
            self->_observing = YES;
        }
        p.active = YES;
        [p present];
    });
}

// pickerOff makes the picker inactive again (nothing more is offered in the menu bar).
- (void)pickerOff {
    dispatch_async(dispatch_get_main_queue(), ^{
        SCContentSharingPicker *p = SCContentSharingPicker.sharedPicker;
        p.active = NO;
        if (self->_observing) {
            [p removeObserver:self];
            self->_observing = NO;
        }
    });
}

- (void)cancelPick {
    @synchronized(self) {
        _picking = NO;
    }
    [self pickerOff];
}

- (void)contentSharingPicker:(SCContentSharingPicker *)picker didUpdateWithFilter:(SCContentFilter *)filter forStream:(SCStream *)stream {
    @synchronized(self) {
        if (!_picking) return;
        _picking = NO;
        _filter = filter;
    }
    CGRect r = filter.contentRect;
    double s = filter.pointPixelScale > 0 ? filter.pointPixelScale : 1;
    tmxrecGoPicked(0, (int)filter.style, (int)(r.size.width * s), (int)(r.size.height * s), NULL);
}

- (void)contentSharingPicker:(SCContentSharingPicker *)picker didCancelForStream:(SCStream *)stream {
    @synchronized(self) {
        if (!_picking) return;
        _picking = NO;
    }
    [self pickerOff];
    tmxrecGoPicked(1, 0, 0, 0, NULL);
}

- (void)contentSharingPickerStartDidFailWithError:(NSError *)error {
    @synchronized(self) {
        if (!_picking) return;
        _picking = NO;
    }
    [self pickerOff];
    char *msg = dupText(errorText(error));
    tmxrecGoPicked(2, 0, 0, 0, msg);
    free(msg);
}

// ---- the stream

- (CMTime)now {
    CMClockRef clk = _stream.synchronizationClock;
    return CMClockGetTime(clk ? clk : CMClockGetHostTimeClock());
}

// open starts the stream on the picked content; *micOn: 1 recording the microphone, 0 not
// asked for, -1 not allowed.
- (NSString *)openFPS:(int)fps maxSide:(int)maxSide system:(BOOL)sys mic:(BOOL)mic micOn:(int *)micOn {
    SCContentFilter *filter;
    @synchronized(self) {
        filter = _filter;
    }
    *micOn = 0;
    if (!filter) return @"nothing was picked to record";
    if (_stream) return @"the capture is already running";
    if (mic) {
        // The owner clicked Start: asking for the microphone now is theirs to answer.
        AVAuthorizationStatus st = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
        if (st == AVAuthorizationStatusNotDetermined) {
            dispatch_semaphore_t sem = dispatch_semaphore_create(0);
            __block BOOL granted = NO;
            [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio completionHandler:^(BOOL ok) {
                granted = ok;
                dispatch_semaphore_signal(sem);
            }];
            dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 120 * NSEC_PER_SEC));
            st = granted ? AVAuthorizationStatusAuthorized : AVAuthorizationStatusDenied;
        }
        *micOn = st == AVAuthorizationStatusAuthorized ? 1 : -1;
    }
    SCStreamConfiguration *c = [SCStreamConfiguration new];
    CGRect r = filter.contentRect;
    double scale = filter.pointPixelScale > 0 ? filter.pointPixelScale : 1;
    double w = r.size.width * scale, h = r.size.height * scale;
    if (w < 16 || h < 16) {
        w = 1920;
        h = 1080;
    }
    double k = MIN(1.0, (double)maxSide / MAX(w, h));
    c.width = MAX((size_t)2, (size_t)(w * k) & ~(size_t)1);
    c.height = MAX((size_t)2, (size_t)(h * k) & ~(size_t)1);
    c.minimumFrameInterval = CMTimeMake(1, fps > 0 ? fps : 15);
    c.pixelFormat = kCVPixelFormatType_32BGRA;
    c.showsCursor = YES;
    c.queueDepth = 6;
    c.capturesAudio = sys;
    c.sampleRate = 48000;
    c.channelCount = 2;
    c.captureMicrophone = *micOn == 1;
    SCStream *stream = [[SCStream alloc] initWithFilter:filter configuration:c delegate:self];
    NSError *err = nil;
    if (![stream addStreamOutput:self type:SCStreamOutputTypeScreen sampleHandlerQueue:_q error:&err] ||
        (sys && ![stream addStreamOutput:self type:SCStreamOutputTypeAudio sampleHandlerQueue:_q error:&err]) ||
        (*micOn == 1 && ![stream addStreamOutput:self type:SCStreamOutputTypeMicrophone sampleHandlerQueue:_q error:&err]))
        return errorText(err);
    TMXSource *sysSrc = sys ? [TMXSource new] : nil, *micSrc = *micOn == 1 ? [TMXSource new] : nil;
    dispatch_sync(_q, ^{
        self->_sys = sysSrc;
        self->_mic = micSrc;
        self->_mixing = NO;
    });
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    __block NSError *startErr = nil;
    [stream startCaptureWithCompletionHandler:^(NSError *e) {
        startErr = e;
        dispatch_semaphore_signal(sem);
    }];
    if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 20 * NSEC_PER_SEC)) != 0) return @"the capture did not start in time";
    if (startErr) return errorText(startErr);
    _closing = NO;
    _stream = stream;
    return nil;
}

- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error {
    if (stream != _stream || _closing) return;
    char *msg = dupText(errorText(error));
    tmxrecGoStopped(error.code, msg);
    free(msg);
}

- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sb ofType:(SCStreamOutputType)type {
    if (!CMSampleBufferIsValid(sb)) return;
    switch (type) {
    case SCStreamOutputTypeScreen:
        [self keepFrame:sb];
        break;
    case SCStreamOutputTypeAudio:
        [self mix:sb from:_sys];
        break;
    case SCStreamOutputTypeMicrophone:
        [self mix:sb from:_mic];
        break;
    }
}

// ---- segments

- (NSString *)startSegment:(NSString *)path {
    if (!_stream) return @"the capture is not running";
    if (_seg) return @"a part is already being recorded";
    SCRecordingOutputConfiguration *rc = [SCRecordingOutputConfiguration new];
    rc.outputURL = [NSURL fileURLWithPath:path];
    rc.outputFileType = AVFileTypeMPEG4;
    rc.videoCodecType = AVVideoCodecTypeH264;
    TMXOut *seg = [TMXOut new];
    seg.ending = YES; // a failure while starting is answered here, not as a stopped stream
    seg.started = dispatch_semaphore_create(0);
    seg.finished = dispatch_semaphore_create(0);
    seg.output = [[SCRecordingOutput alloc] initWithConfiguration:rc delegate:seg];
    CMTime t0 = [self now];
    dispatch_sync(_q, ^{ [self mixResetAt:t0]; }); // the segment's sound starts now
    NSError *err = nil;
    if (![_stream addRecordingOutput:seg.output error:&err]) {
        dispatch_sync(_q, ^{ self->_mixing = NO; });
        return errorText(err);
    }
    if (dispatch_semaphore_wait(seg.started, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC)) != 0 || seg.error) {
        seg.ending = YES;
        [_stream removeRecordingOutput:seg.output error:nil];
        dispatch_sync(_q, ^{ self->_mixing = NO; });
        return seg.error ? errorText(seg.error) : @"the recording did not start in time";
    }
    seg.ending = NO;
    _seg = seg;
    return nil;
}

- (void)progress:(double *)sec bytes:(long long *)bytes {
    *sec = 0;
    *bytes = 0;
    if (!_seg) return;
    CMTime d = _seg.output.recordedDuration;
    *sec = CMTIME_IS_NUMERIC(d) ? CMTimeGetSeconds(d) : 0;
    *bytes = (long long)_seg.output.recordedFileSize;
}

// endSegment finishes the open segment's MP4 (removing the output writes its index) and
// delivers its last sound, padded to the segment's length.
- (NSString *)endSegment:(double *)sec {
    *sec = 0;
    TMXOut *seg = _seg;
    if (!seg) return nil;
    _seg = nil;
    CMTime end = [self now];
    dispatch_sync(_q, ^{ [self mixFinishAt:end]; });
    seg.ending = YES;
    NSString *problem = nil;
    if (!seg.didFinish) {
        NSError *err = nil;
        if (_stream && ![_stream removeRecordingOutput:seg.output error:&err]) problem = errorText(err);
        else if (dispatch_semaphore_wait(seg.finished, dispatch_time(DISPATCH_TIME_NOW, 15 * NSEC_PER_SEC)) != 0)
            problem = @"the recording's file did not finish in time";
    }
    if (!problem && seg.error) problem = errorText(seg.error);
    CMTime d = seg.output.recordedDuration;
    *sec = CMTIME_IS_NUMERIC(d) ? CMTimeGetSeconds(d) : 0;
    return problem;
}

- (void)close {
    if (_seg) {
        double s;
        [self endSegment:&s];
    }
    if (_stream) {
        _closing = YES;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);
        [_stream stopCaptureWithCompletionHandler:^(NSError *e) { dispatch_semaphore_signal(sem); }];
        dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));
        _stream = nil;
    }
    dispatch_sync(_q, ^{
        self->_mixing = NO;
        self->_sys = nil;
        self->_mic = nil;
    });
    os_unfair_lock_lock(&_frameLock);
    if (_latest) CVPixelBufferRelease(_latest);
    _latest = NULL;
    os_unfair_lock_unlock(&_frameLock);
    @synchronized(self) {
        _filter = nil;
        _picking = NO;
    }
    [self pickerOff];
}

// ---- the mixer (on _q): both sources go to 16 kHz mono and are added where their time
// stamps say, so a source that pauses (nothing playing) leaves silence, not a shift.

- (void)mixResetAt:(CMTime)t0 {
    memset(_acc, 0, ACC * sizeof(float));
    _base = _hi = 0;
    _t0 = t0;
    _sys.started = _mic.started = NO;
    _mixing = YES;
}

- (AVAudioPCMBuffer *)convert:(CMSampleBufferRef)sb with:(TMXSource *)src {
    CMFormatDescriptionRef fd = CMSampleBufferGetFormatDescription(sb);
    const AudioStreamBasicDescription *asbd = fd ? CMAudioFormatDescriptionGetStreamBasicDescription(fd) : NULL;
    if (!asbd || asbd->mSampleRate <= 0) return nil;
    AudioStreamBasicDescription have = src.asbd;
    if (!src.conv || memcmp(&have, asbd, sizeof have) != 0) {
        size_t layoutSize = 0;
        const AudioChannelLayout *layout = CMAudioFormatDescriptionGetChannelLayout(fd, &layoutSize);
        AVAudioChannelLayout *cl = layout ? [[AVAudioChannelLayout alloc] initWithLayout:layout] : nil;
        AVAudioFormat *f = [[AVAudioFormat alloc] initWithStreamDescription:asbd channelLayout:cl];
        if (!f) f = [[AVAudioFormat alloc] initWithStreamDescription:asbd];
        AVAudioConverter *c = f ? [[AVAudioConverter alloc] initFromFormat:f toFormat:_out16] : nil;
        if (!c) return nil;
        c.downmix = YES; // stereo (or more) is mixed down, not just its first channel taken
        c.sampleRateConverterQuality = AVAudioQualityHigh;
        src.asbd = *asbd;
        src.inFormat = f;
        src.conv = c;
    }
    CMItemCount n = CMSampleBufferGetNumSamples(sb);
    if (n <= 0) return nil;
    AVAudioPCMBuffer *in = [[AVAudioPCMBuffer alloc] initWithPCMFormat:src.inFormat frameCapacity:(AVAudioFrameCount)n];
    if (!in) return nil;
    in.frameLength = (AVAudioFrameCount)n;
    if (CMSampleBufferCopyPCMDataIntoAudioBufferList(sb, 0, (int32_t)n, in.mutableAudioBufferList) != noErr) return nil;
    AVAudioFrameCount cap = (AVAudioFrameCount)((double)n * RATE / src.inFormat.sampleRate) + 64;
    AVAudioPCMBuffer *out = [[AVAudioPCMBuffer alloc] initWithPCMFormat:_out16 frameCapacity:cap];
    __block BOOL fed = NO;
    NSError *err = nil;
    AVAudioConverterOutputStatus st = [src.conv convertToBuffer:out error:&err withInputFromBlock:^AVAudioBuffer *(AVAudioPacketCount count, AVAudioConverterInputStatus *status) {
        if (fed) {
            *status = AVAudioConverterInputStatus_NoDataNow;
            return nil;
        }
        fed = YES;
        *status = AVAudioConverterInputStatus_HaveData;
        return in;
    }];
    return st == AVAudioConverterOutputStatus_Error ? nil : out;
}

- (void)mix:(CMSampleBufferRef)sb from:(TMXSource *)src {
    if (!_mixing || !src) return;
    AVAudioPCMBuffer *out = [self convert:sb with:src];
    if (!out || out.frameLength == 0) return;
    CMTime pts = CMSampleBufferGetPresentationTimeStamp(sb);
    int64_t pos = src.next;
    if (CMTIME_IS_NUMERIC(pts)) pos = llround(CMTimeGetSeconds(CMTimeSubtract(pts, _t0)) * RATE);
    if (!src.started) {
        // The first buffer: trust its time unless it is far off (another clock), then start here.
        src.next = (pos > -RATE && pos < _hi + RATE * 60) ? pos : _hi;
        src.started = YES;
    } else if (llabs(pos - src.next) > RATE / 20 && llabs(pos - src.next) < RATE * 30) {
        src.next = pos; // a gap (nothing came for a while) or drift: follow the clock
    }
    src.heardAt = mach_absolute_time();
    const float *x = out.floatChannelData[0];
    int64_t n = out.frameLength, at = src.next;
    src.next = at + n;
    if (at < _base) { // before what was already written out
        int64_t skip = _base - at;
        if (skip >= n) return;
        x += skip;
        n -= skip;
        at = _base;
    }
    if (at + n - _base > ACC) [self mixEmit:at + n - ACC]; // make room: older sound goes out
    float *dst = _acc + (at - _base);
    for (int64_t i = 0; i < n; i++) dst[i] += x[i];
    if (at + n > _hi) _hi = at + n;
    // Write out what every live source has delivered (a source quiet for 300 ms waits for nobody).
    uint64_t now = mach_absolute_time();
    int64_t ready = INT64_MAX;
    TMXSource *sources[2] = {_sys, _mic};
    for (int i = 0; i < 2; i++) {
        TMXSource *s = sources[i];
        if (s && s.started && hostSeconds(now - s.heardAt) < 0.3) ready = MIN(ready, s.next);
    }
    if (ready == INT64_MAX) ready = _hi;
    if (ready - _base >= RATE / 10) [self mixEmit:ready];
}

// mixEmit sends the sound before sample upto to Go as PCM16 (silence where nothing came).
- (void)mixEmit:(int64_t)upto {
    if (upto <= _base) return;
    int64_t n = upto - _base;
    for (int64_t done = 0; done < n;) {
        int64_t m = MIN((int64_t)RATE, n - done);
        for (int64_t i = 0; i < m; i++) {
            int64_t k = done + i;
            float v = k < ACC ? _acc[k] : 0;
            v = v > 1 ? 1 : v < -1 ? -1 : v;
            _chunk[i] = (int16_t)lrintf(v * 32767);
        }
        tmxrecGoPCM(_chunk, (int)m);
        done += m;
    }
    if (n < ACC) {
        memmove(_acc, _acc + n, (size_t)(ACC - n) * sizeof(float));
        memset(_acc + (ACC - n), 0, (size_t)n * sizeof(float));
    } else {
        memset(_acc, 0, ACC * sizeof(float));
    }
    _base = upto;
    if (_hi < _base) _hi = _base;
}

// mixFinishAt writes out the rest, padded with silence to the segment's end, so the WAV is as
// long as the segment.
- (void)mixFinishAt:(CMTime)end {
    if (!_mixing) return;
    int64_t endPos = llround(CMTimeGetSeconds(CMTimeSubtract(end, _t0)) * RATE);
    if (endPos < 0 || endPos > _hi + (int64_t)RATE * 3600) endPos = _hi;
    [self mixEmit:MAX(_hi, endPos)];
    _mixing = NO;
}

// ---- frames

- (void)keepFrame:(CMSampleBufferRef)sb {
    CFArrayRef atts = CMSampleBufferGetSampleAttachmentsArray(sb, false);
    if (!atts || CFArrayGetCount(atts) == 0) return;
    NSDictionary *a = (__bridge NSDictionary *)CFArrayGetValueAtIndex(atts, 0);
    NSNumber *status = a[SCStreamFrameInfoStatus];
    if (!status || status.integerValue != SCFrameStatusComplete) return; // idle frames carry no image
    CVPixelBufferRef pb = CMSampleBufferGetImageBuffer(sb);
    if (!pb) return;
    CVPixelBufferRetain(pb);
    os_unfair_lock_lock(&_frameLock);
    CVPixelBufferRef old = _latest;
    _latest = pb;
    os_unfair_lock_unlock(&_frameLock);
    if (old) CVPixelBufferRelease(old);
}

// jpeg is the newest frame, longest side at most 1280 px, at most 600 KB (quality 0.7, less
// when it is larger); nil before the first frame.
- (NSData *)jpeg {
    os_unfair_lock_lock(&_frameLock);
    CVPixelBufferRef pb = _latest;
    if (pb) CVPixelBufferRetain(pb);
    os_unfair_lock_unlock(&_frameLock);
    if (!pb) return nil;
    CGImageRef img = NULL;
    CVPixelBufferLockBaseAddress(pb, kCVPixelBufferLock_ReadOnly);
    void *base = CVPixelBufferGetBaseAddress(pb);
    if (base && CVPixelBufferGetPixelFormatType(pb) == kCVPixelFormatType_32BGRA) {
        CGColorSpaceRef cs = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
        CGContextRef ctx = CGBitmapContextCreate(base, CVPixelBufferGetWidth(pb), CVPixelBufferGetHeight(pb), 8,
                                                 CVPixelBufferGetBytesPerRow(pb), cs,
                                                 (CGBitmapInfo)kCGImageAlphaNoneSkipFirst | kCGBitmapByteOrder32Little);
        if (ctx) {
            img = CGBitmapContextCreateImage(ctx); // a copy: the buffer goes back to the stream
            CGContextRelease(ctx);
        }
        CGColorSpaceRelease(cs);
    }
    CVPixelBufferUnlockBaseAddress(pb, kCVPixelBufferLock_ReadOnly);
    CVPixelBufferRelease(pb);
    if (!img) return nil;
    NSData *best = nil;
    for (double quality = 0.7; quality > 0.2; quality -= 0.15) {
        NSMutableData *d = [NSMutableData data];
        CGImageDestinationRef dst = CGImageDestinationCreateWithData((__bridge CFMutableDataRef)d, CFSTR("public.jpeg"), 1, NULL);
        if (!dst) break;
        NSDictionary *props = @{
            (__bridge NSString *)kCGImageDestinationLossyCompressionQuality : @(quality),
            (__bridge NSString *)kCGImageDestinationImageMaxPixelSize : @1280,
        };
        CGImageDestinationAddImage(dst, img, (__bridge CFDictionaryRef)props);
        BOOL ok = CGImageDestinationFinalize(dst);
        CFRelease(dst);
        if (!ok) break;
        best = d;
        if (d.length <= MAX_JPEG) break;
    }
    CGImageRelease(img);
    return best.length <= MAX_JPEG ? best : nil;
}

@end

// ---------------------------------------------------------------------------------------------
// The C functions Go calls (capture_darwin.go).

API_AVAILABLE(macos(15.0))
static TMXRec *shared(void) {
    static TMXRec *rec;
    static dispatch_once_t once;
    dispatch_once(&once, ^{ rec = [TMXRec new]; });
    return rec;
}

int tmxrecSupported(void) {
    if (@available(macOS 15.0, *)) {
        return NSClassFromString(@"SCRecordingOutput") != nil && NSClassFromString(@"SCContentSharingPicker") != nil;
    }
    return 0;
}

void tmxrecPick(void) {
    if (@available(macOS 15.0, *)) {
        [shared() pick];
        return;
    }
    tmxrecGoPicked(2, 0, 0, 0, "native screen recording needs macOS 15 or later");
}

void tmxrecPickCancel(void) {
    if (@available(macOS 15.0, *)) {
        [shared() cancelPick];
    }
}

int tmxrecOpen(int fps, int maxSide, int systemAudio, int mic, int *micOn, char **err) {
    *micOn = 0;
    if (@available(macOS 15.0, *)) {
        NSString *e = [shared() openFPS:fps maxSide:maxSide system:systemAudio != 0 mic:mic != 0 micOn:micOn];
        *err = dupText(e);
        return e ? -1 : 0;
    }
    *err = dupText(@"native screen recording needs macOS 15 or later");
    return -1;
}

int tmxrecStartSegment(const char *path, char **err) {
    if (@available(macOS 15.0, *)) {
        NSString *e = [shared() startSegment:[NSString stringWithUTF8String:path]];
        *err = dupText(e);
        return e ? -1 : 0;
    }
    *err = dupText(@"native screen recording needs macOS 15 or later");
    return -1;
}

void tmxrecProgress(double *sec, long long *bytes) {
    *sec = 0;
    *bytes = 0;
    if (@available(macOS 15.0, *)) {
        [shared() progress:sec bytes:bytes];
    }
}

int tmxrecEndSegment(double *sec, char **err) {
    *sec = 0;
    *err = NULL;
    if (@available(macOS 15.0, *)) {
        NSString *e = [shared() endSegment:sec];
        *err = dupText(e);
        return e ? -1 : 0;
    }
    return 0;
}

// tmxrecFrame: 0 with a malloc'd JPEG in *data, 1 when there is no frame yet.
int tmxrecFrame(void **data, int *len) {
    *data = NULL;
    *len = 0;
    if (@available(macOS 15.0, *)) {
        @autoreleasepool {
            NSData *d = [shared() jpeg];
            if (!d.length) return 1;
            *data = malloc(d.length);
            memcpy(*data, d.bytes, d.length);
            *len = (int)d.length;
            return 0;
        }
    }
    return 1;
}

void tmxrecClose(void) {
    if (@available(macOS 15.0, *)) {
        [shared() close];
    }
}
