//go:build android && cgo
#include <jni.h>
#include <stdlib.h>
#include <string.h>
#include "_cgo_export.h"

JNIEXPORT jbyteArray JNICALL Java_com_wails_app_AppUpdater_runNative(JNIEnv *env, jclass cls, jstring action, jstring dir, jbyteArray envelope, jlong code, jint sdk) {
    const char *command = (*env)->GetStringUTFChars(env, action, NULL);
    const char *path = (*env)->GetStringUTFChars(env, dir, NULL);
    jsize length = (*env)->GetArrayLength(env, envelope);
    char *json = calloc((size_t)length + 1, 1);
    if (!command || !path || !json) return NULL;
    (*env)->GetByteArrayRegion(env, envelope, 0, length, (jbyte*)json);
    char *result = AndroidUpdate((char*)command, (char*)path, json, code, sdk);
    (*env)->ReleaseStringUTFChars(env, action, command);
    (*env)->ReleaseStringUTFChars(env, dir, path);
    free(json);
    jsize n = (jsize)strlen(result);
    jbyteArray bytes = (*env)->NewByteArray(env,n);
    if (bytes) (*env)->SetByteArrayRegion(env,bytes,0,n,(jbyte*)result);
    free(result);
    return bytes;
}
JNIEXPORT void JNICALL Java_com_wails_app_AppUpdater_control(JNIEnv *env,jclass cls,jboolean start){AndroidDownloadControl(start);}
JNIEXPORT jlong JNICALL Java_com_wails_app_AppUpdater_received(JNIEnv *env,jclass cls){return AndroidDownloaded();}
