#include <CoreServices/CoreServices.h>
#include <CoreFoundation/CoreFoundation.h>
#include <sys/stat.h>
#include <sys/time.h>

static void printChangesFunc(ConstFSEventStreamRef streamRef, void *clientCallBackInfo, size_t numEvents, void *eventPaths, const FSEventStreamEventFlags eventFlags[], const FSEventStreamEventId eventIds[]) {
	char **paths = eventPaths;
	struct timeval tv;

	int i;
	for (i = 0; i < numEvents; i++) {
		puts(paths[i]);
	}
	fflush(stdout);
}

void initFSEvents(const char *path) {
	
	/* Define variables and create a CFArray object containing
	 CFString objects containing paths to watch.
	 */
	
	CFStringRef mypath = CFStringCreateWithCString(NULL, path, kCFStringEncodingUTF8);
	CFArrayRef pathsToWatch = CFArrayCreate(NULL, (const void **)&mypath, 1, NULL);
	void *callbackInfo = NULL; // could put stream-specific data here.
	FSEventStreamRef stream;
	CFAbsoluteTime latency = 0.0; /* Latency in seconds */
	
	/* Create the stream, passing in a callback */
	stream = FSEventStreamCreate(NULL,
	                             &printChangesFunc,
	                             callbackInfo,
	                             pathsToWatch,
	                             kFSEventStreamEventIdSinceNow, /* Or a previous event ID */
	                             latency,
	                             kFSEventStreamCreateFlagNone|kFSEventStreamCreateFlagNoDefer /* Flags explained in reference */
	                             );
	
	CFRelease(pathsToWatch);
	CFRelease(mypath);
	
	/* Create the stream before calling this. */
	FSEventStreamScheduleWithRunLoop(stream, CFRunLoopGetCurrent(), kCFRunLoopDefaultMode);
	FSEventStreamStart(stream);
}

int main (int argc, const char * argv[]) {
	
	if(argc != 2) {
		printf("Usage: %s <path>\n", argv[0]);
		return 1;
	}
	
	struct stat tmp;
	
	if(stat(argv[1], &tmp) != 0) {
		perror("Invalid path");
		return 2;
	}
	
	initFSEvents(argv[1]);
	puts("Initialized");
	fflush(stdout);
	CFRunLoopRun();
	
	return 0;
}
