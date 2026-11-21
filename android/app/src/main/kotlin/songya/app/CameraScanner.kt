package songya.app

import android.content.Context
import androidx.camera.core.CameraSelector
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.Preview
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.view.PreviewView
import androidx.core.content.ContextCompat
import androidx.lifecycle.LifecycleOwner
import com.google.zxing.BarcodeFormat
import com.google.zxing.BinaryBitmap
import com.google.zxing.DecodeHintType
import com.google.zxing.MultiFormatReader
import com.google.zxing.PlanarYUVLuminanceSource
import com.google.zxing.common.HybridBinarizer
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

internal object QrDecoder {
    fun decode(bytes: ByteArray, width: Int, height: Int): String? = runCatching {
        val source = PlanarYUVLuminanceSource(bytes, width, height, 0, 0, width, height, false)
        MultiFormatReader().decode(BinaryBitmap(HybridBinarizer(source)), mapOf(
            DecodeHintType.POSSIBLE_FORMATS to listOf(BarcodeFormat.QR_CODE),
            DecodeHintType.TRY_HARDER to true,
        )).text
    }.getOrNull()
}
internal class CameraScanner(
    context: Context,
    owner: LifecycleOwner,
    previewView: PreviewView,
    onResult: (String) -> Unit,
    onError: () -> Unit,
) : AutoCloseable {
    private val stopped = AtomicBoolean(false)
    private val executor = Executors.newSingleThreadExecutor()
    private var provider: ProcessCameraProvider? = null
    init {
        val future = ProcessCameraProvider.getInstance(context)
        val main = ContextCompat.getMainExecutor(context)
        future.addListener({
            if (!stopped.get()) {
                runCatching {
                    provider = future.get()
                    val preview = Preview.Builder().build().apply { setSurfaceProvider(previewView.surfaceProvider) }
                    val analysis = ImageAnalysis.Builder().setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST).build()
                    analysis.setAnalyzer(executor) { image ->
                        try {
                            if (!stopped.get()) {
                                val plane = image.planes[0]
                                val bytes = ByteArray(image.width * image.height)
                                for (y in 0 until image.height) {
                                    plane.buffer.position(y * plane.rowStride)
                                    if (plane.pixelStride == 1) plane.buffer.get(bytes, y * image.width, image.width)
                                    else for (x in 0 until image.width) bytes[y * image.width + x] = plane.buffer.get(y * plane.rowStride + x * plane.pixelStride)
                                }
                                val value = QrDecoder.decode(bytes, image.width, image.height)
                                if (value != null && stopped.compareAndSet(false, true)) main.execute { onResult(value) }
                            }
                        } finally { image.close() }
                    }
                    provider!!.bindToLifecycle(owner, CameraSelector.DEFAULT_BACK_CAMERA, preview, analysis)
                }.onFailure { onError() }
            }
        }, main)
    }
    override fun close() {
        stopped.set(true)
        provider?.unbindAll()
        executor.shutdownNow()
    }
}
