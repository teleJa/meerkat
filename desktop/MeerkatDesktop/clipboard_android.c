//go:build android && cgo

#include <jni.h>
#include <pthread.h>

static pthread_mutex_t clipboard_mutex = PTHREAD_MUTEX_INITIALIZER;
static JavaVM *clipboard_vm;
static jobject clipboard_bridge;
static jmethodID clipboard_write;

JNIEXPORT void JNICALL Java_com_wails_app_WailsBridge_nativeBindMeerkatClipboard(JNIEnv *env, jobject bridge) {
    JavaVM *vm = NULL;
    if ((*env)->GetJavaVM(env, &vm) != JNI_OK) return;
    jclass type = (*env)->GetObjectClass(env, bridge);
    if (type == NULL) return;
    jmethodID write = (*env)->GetMethodID(env, type, "setMeerkatClipboardText", "(Ljava/lang/String;)Z");
    (*env)->DeleteLocalRef(env, type);
    if (write == NULL) return;
    jobject ref = (*env)->NewGlobalRef(env, bridge);
    if (ref == NULL) return;

    pthread_mutex_lock(&clipboard_mutex);
    if (clipboard_bridge != NULL) (*env)->DeleteGlobalRef(env, clipboard_bridge);
    clipboard_vm = vm;
    clipboard_bridge = ref;
    clipboard_write = write;
    pthread_mutex_unlock(&clipboard_mutex);
}

JNIEXPORT void JNICALL Java_com_wails_app_WailsBridge_nativeUnbindMeerkatClipboard(JNIEnv *env, jobject bridge) {
    pthread_mutex_lock(&clipboard_mutex);
    if (clipboard_bridge != NULL && (*env)->IsSameObject(env, bridge, clipboard_bridge)) {
        (*env)->DeleteGlobalRef(env, clipboard_bridge);
        clipboard_bridge = NULL;
    }
    pthread_mutex_unlock(&clipboard_mutex);
}

int meerkat_write_clipboard(const jchar *text, jsize length) {
    pthread_mutex_lock(&clipboard_mutex);
    JavaVM *vm = clipboard_vm;
    pthread_mutex_unlock(&clipboard_mutex);
    if (vm == NULL) return 0;

    JNIEnv *env = NULL;
    jint status = (*vm)->GetEnv(vm, (void **)&env, JNI_VERSION_1_6);
    if (status == JNI_EDETACHED && (*vm)->AttachCurrentThread(vm, &env, NULL) != JNI_OK) return 0;
    if (env == NULL) return 0;

    pthread_mutex_lock(&clipboard_mutex);
    jobject bridge = clipboard_bridge == NULL ? NULL : (*env)->NewLocalRef(env, clipboard_bridge);
    jmethodID write = clipboard_write;
    pthread_mutex_unlock(&clipboard_mutex);

    jboolean result = JNI_FALSE;
    if (bridge != NULL) {
        jstring value = (*env)->NewString(env, text, length);
        if (value != NULL) {
            result = (*env)->CallBooleanMethod(env, bridge, write, value);
            (*env)->DeleteLocalRef(env, value);
        }
        (*env)->DeleteLocalRef(env, bridge);
    }
    if ((*env)->ExceptionCheck(env)) {
        (*env)->ExceptionClear(env);
        result = JNI_FALSE;
    }
    if (status == JNI_EDETACHED) (*vm)->DetachCurrentThread(vm);
    return result == JNI_TRUE;
}
