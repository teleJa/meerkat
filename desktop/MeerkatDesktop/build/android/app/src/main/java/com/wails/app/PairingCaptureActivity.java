package com.wails.app;

import android.os.Bundle;
import android.view.View;

import androidx.core.view.ViewCompat;
import androidx.core.view.WindowInsetsCompat;

import com.journeyapps.barcodescanner.CaptureActivity;

public final class PairingCaptureActivity extends CaptureActivity {
    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        View prompt = findViewById(com.google.zxing.client.android.R.id.zxing_status_view);
        ViewCompat.setOnApplyWindowInsetsListener(prompt, (view, insets) -> {
            view.setTranslationY(-insets.getInsets(WindowInsetsCompat.Type.navigationBars()).bottom);
            return insets;
        });
        ViewCompat.requestApplyInsets(prompt);
    }
}
